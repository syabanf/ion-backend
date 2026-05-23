package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/audit"
)

// =====================================================================
// Wave 108 — Edge #22: audit chain tamper detection
//
// VerifyChain walks identity.audit_logs in (timestamp ASC, id ASC) and
// recomputes each row's row_hash from (prev_hash || canonical-payload).
// A tampered row — one whose stored row_hash no longer matches the
// recompute — must:
//   - increment ChainVerifyResult.Broken
//   - set ChainVerifyResult.FirstBrokenID to the FIRST divergent row
//     in walk order (subsequent breakages don't overwrite it)
//
// This test drives the verifier end-to-end against a live pg:
//   1. Insert two audit rows via the production Writer (so the
//      BEFORE-INSERT trigger fills prev_hash + row_hash naturally).
//   2. Use a raw UPDATE to corrupt the FIRST row's stored row_hash.
//      The append-only trigger from migration 0070 would normally
//      block UPDATEs, so we wrap the UPDATE in SET LOCAL session_replication_role
//      to disable triggers for one transaction. This is the same dance
//      a malicious DBA would have to perform — and the verifier must
//      still catch it.
//   3. Call VerifyChain over a window covering both rows and assert
//      Broken==1, FirstBrokenID is set to the tampered row's id.
//
// DB-required; t.Skip on no DATABASE_URL.
// =====================================================================

func TestVerifyChain_DetectsTamperedRowHash(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping audit chain tamper DB test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("could not connect to DB: %v", err)
	}
	t.Cleanup(pool.Close)

	// Sanity — both halves of the schema must exist for this to fly.
	var hasTrigger bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pg_trigger
			WHERE tgname = 'audit_chain_bi_trigger'
		)
	`).Scan(&hasTrigger)
	if err != nil {
		t.Skipf("could not introspect pg_trigger: %v", err)
	}
	if !hasTrigger {
		t.Skip("audit_chain_bi_trigger missing — migration 0070 not applied; skipping")
	}

	writer := NewWriter(pool)
	reader := NewReader(pool)

	// Use a unique record_type token so the verifier window picks ONLY
	// the rows we just inserted, not arbitrary pre-existing audit rows
	// in a shared-CI database.
	tag := "wave108_tamper_" + uuid.New().String()[:8]
	uid := uuid.New()

	// Mark the start of our window BEFORE the first insert. Use the
	// server clock so it stays consistent with the trigger's NOW().
	var startTS time.Time
	if err := pool.QueryRow(ctx, "SELECT NOW()").Scan(&startTS); err != nil {
		t.Fatalf("clock read: %v", err)
	}
	// Step back a second to give the writer room.
	startTS = startTS.Add(-1 * time.Second)

	// 1. Insert two rows via the production Writer.
	for i := 0; i < 2; i++ {
		err := writer.Write(ctx, audit.Entry{
			UserID:     uid,
			Module:     "wave108",
			RecordType: tag,
			RecordID:   uuid.New().String(),
			After:      "v" + time.Now().UTC().Format("150405.000000"),
			Reason:     "tamper-detection-fixture",
		})
		if err != nil {
			t.Fatalf("Write row %d: %v", i, err)
		}
	}

	// 2. Locate the first row in walk order so we can corrupt its hash.
	var firstID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT id FROM identity.audit_logs
		WHERE record_type = $1
		ORDER BY timestamp ASC, id ASC
		LIMIT 1
	`, tag).Scan(&firstID)
	if err != nil {
		t.Fatalf("locate first row: %v", err)
	}

	// 3. Tamper. The append-only trigger blocks plain UPDATEs by
	//    design, so we have to bypass it for this single statement to
	//    simulate a backdoor / DBA escape. session_replication_role =
	//    'replica' disables BEFORE/AFTER triggers for the connection
	//    until we flip it back. We bind the operation to one acquired
	//    connection so the disable doesn't leak into other tests.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET LOCAL session_replication_role = 'replica'"); err != nil {
		// Some managed-pg flavors deny SET session_replication_role to
		// non-superusers. In that case the trigger genuinely is the
		// only defense; skip the rest of the test (the migration's
		// append-only enforcement is what we'd be testing here, and
		// it works as designed).
		t.Skipf("cannot disable triggers (likely non-superuser): %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	tamperedHash := "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := tx.Exec(ctx, `
		UPDATE identity.audit_logs SET row_hash = $1 WHERE id = $2
	`, tamperedHash, firstID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("tamper UPDATE: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit tamper: %v", err)
	}

	// 4. Verify.
	endTS := time.Now().UTC().Add(1 * time.Second)
	result, err := reader.VerifyChain(ctx, startTS, endTS)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if result.Broken == 0 {
		t.Fatalf("Broken = 0; want >= 1 (chain tamper undetected)")
	}
	if result.FirstBrokenID == nil {
		t.Fatal("FirstBrokenID = nil; want first divergent row id set")
	}
	if *result.FirstBrokenID != firstID {
		t.Errorf("FirstBrokenID = %v, want %v (the tampered row)",
			*result.FirstBrokenID, firstID)
	}
	if result.Total < 2 {
		t.Errorf("Total = %d, want >= 2", result.Total)
	}
}
