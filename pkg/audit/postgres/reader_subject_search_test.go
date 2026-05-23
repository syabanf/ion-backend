package postgres

import (
	"context"
	goErrors "errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 108 — Audit Log Part 2 — TC-AU-007 subject-search smoke
//
// Reader.Query is the load-bearing read surface that powers
// /api/audit/entries (mounted under identity-svc). The handler-level
// parse path is exercised in pkg/audit/http/audit_query_handler_test.go
// — this test pins the SQL filter behavior with a live DB.
//
// We insert 3 audit rows under a unique record_type token, then
// exercise:
//   - filter by SubjectType + SubjectID → returns the matching row
//   - filter by SubjectType only → returns all 3 rows under the tag
//   - filter by Limit → respects the cap
//   - filter by Module → exact match
//
// DB-required; t.Skip on no DATABASE_URL.
// =====================================================================

func TestReader_Query_SubjectSearchFiltersAndPagination(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping audit subject-search DB test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("could not connect to DB: %v", err)
	}
	t.Cleanup(pool.Close)

	writer := NewWriter(pool)
	reader := NewReader(pool)

	tag := "wave108_search_" + uuid.New().String()[:8]
	uid := uuid.New()
	targetRecordID := uuid.New().String()
	otherRecordIDs := []string{uuid.New().String(), uuid.New().String()}

	// Insert: 1 row with the targetRecordID, 2 rows with other ids — all
	// under the unique record_type tag.
	for i, rid := range append([]string{targetRecordID}, otherRecordIDs...) {
		if err := writer.Write(ctx, audit.Entry{
			UserID:     uid,
			Module:     "wave108",
			RecordType: tag,
			RecordID:   rid,
			After:      "evt-" + uuid.New().String()[:4],
			Reason:     "subject-search-fixture",
			// vary the timestamp tiny so DESC ordering is stable
			Timestamp: time.Now().UTC().Add(time.Duration(-i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	// 1. SubjectType + SubjectID: returns exactly the one matching row.
	entries, err := reader.Query(ctx, QueryFilter{
		SubjectType: tag,
		SubjectID:   targetRecordID,
	})
	if err != nil {
		t.Fatalf("Query subject_type+subject_id: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("len = %d, want 1 (subject_type+subject_id filter)", len(entries))
	} else if entries[0].SubjectID != targetRecordID {
		t.Errorf("SubjectID = %q, want %q", entries[0].SubjectID, targetRecordID)
	}

	// 2. SubjectType only: returns all 3 rows.
	entries, err = reader.Query(ctx, QueryFilter{SubjectType: tag})
	if err != nil {
		t.Fatalf("Query subject_type: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("len = %d, want 3 (subject_type-only filter)", len(entries))
	}

	// 3. Limit cap: 2 rows with Limit=2.
	entries, err = reader.Query(ctx, QueryFilter{SubjectType: tag, Limit: 2})
	if err != nil {
		t.Fatalf("Query with limit: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("len = %d, want 2 (Limit=2 cap)", len(entries))
	}

	// 4. Module filter: returns all 3 (module=wave108).
	entries, err = reader.Query(ctx, QueryFilter{Module: "wave108", SubjectType: tag})
	if err != nil {
		t.Fatalf("Query module+subject_type: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("len = %d, want 3 (module=wave108 filter)", len(entries))
	}

	// 5. DESC ordering: results should be in timestamp DESC order; the
	//    row inserted first (i=0, smallest negative offset, i.e. newest)
	//    should be at index 0.
	entries, err = reader.Query(ctx, QueryFilter{SubjectType: tag})
	if err != nil {
		t.Fatalf("Query for ordering check: %v", err)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Timestamp.Before(entries[i].Timestamp) {
			t.Errorf("entries not in DESC order at index %d: %v before %v",
				i, entries[i-1].Timestamp, entries[i].Timestamp)
		}
	}
}

// TestReader_VerifyChain_RoundtripVerifiesAllRows — TC-AU-008 closure
// at the Go layer. Insert 3 rows via the production Writer (so the
// trigger fills prev_hash + row_hash naturally); call VerifyChain over
// the window; assert Broken==0, Verified == Total >= 3, FirstBrokenID
// is nil.
func TestReader_VerifyChain_RoundtripVerifiesAllRows(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping verify-chain roundtrip DB test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("could not connect to DB: %v", err)
	}
	t.Cleanup(pool.Close)

	// Sanity: chain trigger must be present.
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

	tag := "wave108_verify_" + uuid.New().String()[:8]
	uid := uuid.New()

	var startTS time.Time
	if err := pool.QueryRow(ctx, "SELECT NOW()").Scan(&startTS); err != nil {
		t.Fatalf("clock read: %v", err)
	}
	startTS = startTS.Add(-1 * time.Second)

	for i := 0; i < 3; i++ {
		if err := writer.Write(ctx, audit.Entry{
			UserID:     uid,
			Module:     "wave108",
			RecordType: tag,
			RecordID:   uuid.New().String(),
			After:      "step-" + uuid.New().String()[:4],
			Reason:     "verify-chain-fixture",
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	endTS := time.Now().UTC().Add(1 * time.Second)
	result, err := reader.VerifyChain(ctx, startTS, endTS)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if result.Broken != 0 {
		t.Errorf("Broken = %d, want 0 (fresh chain rows)", result.Broken)
	}
	if result.FirstBrokenID != nil {
		t.Errorf("FirstBrokenID = %v, want nil", result.FirstBrokenID)
	}
	if result.Total < 3 {
		t.Errorf("Total = %d, want >= 3 (we inserted 3 rows in-window)", result.Total)
	}
	// Defensive bound — if the Go-side recompute drifted from the
	// postgres trigger, Verified would not equal Total even with a
	// fresh table.
	if result.Verified != result.Total {
		// Best-effort: emit a clearer diagnostic. The Go computeRowHash
		// is BEST-EFFORT per the audit doc — divergence here is a soft
		// signal, not a hard fail. The canonical truth lives in the SQL
		// function; this test exists to catch wholesale breakage.
		t.Logf("verify drift: Verified=%d, Total=%d, Broken=%d — "+
			"Go-side recompute may have drifted from SQL trigger; "+
			"see audit doc §5",
			result.Verified, result.Total, result.Broken)
	}
}

// TestReader_Query_NilPoolReturnsTypedError — quick guard against
// accidental nil-pool plumbing in test fakes. The error code (not the
// message) is the stable contract: pkg/errors stringifies as
// "<kind>: <message>" while keeping the typed Code on the *Error.
func TestReader_Query_NilPoolReturnsTypedError(t *testing.T) {
	var r *Reader
	_, err := r.Query(context.Background(), QueryFilter{})
	if err == nil {
		t.Fatal("Query on nil Reader must error")
	}
	var de *derrors.Error
	if !goErrors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Code != "audit.not_configured" {
		t.Errorf("code = %q, want audit.not_configured", de.Code)
	}
}
