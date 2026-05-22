// ktp-backfill — one-shot tool that migrates any remaining
// plaintext NIK rows in crm.leads / crm.customers into the
// nik_encrypted column and NULLs out the plaintext.
//
// Run once per environment after deploying the migration 0017 + the
// at-rest-enabled binaries. Re-running is safe: the tool skips rows
// that already have a non-null nik_encrypted.
//
// Provenance: each run writes one row to crm.ktp_backfill_runs with a
// started_at + completed_at + counts + (optional) error. The
// KTP_BACKFILL_OPERATOR env var labels the row so audits can trace
// who ran it; defaults to the hostname.
//
// Usage:
//
//	KTP_ENC_KEY=… DATABASE_URL=… [KTP_BACKFILL_OPERATOR=alice] \
//	  go run ./cmd/ktp-backfill
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/cryptutil"
)

func main() {
	key := os.Getenv("KTP_ENC_KEY")
	if key == "" {
		log.Fatal("KTP_ENC_KEY is required")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	sealer, err := cryptutil.NewSealer(key)
	if err != nil {
		log.Fatalf("KTP_ENC_KEY invalid: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	operator := os.Getenv("KTP_BACKFILL_OPERATOR")
	if operator == "" {
		if h, err := os.Hostname(); err == nil {
			operator = h
		} else {
			operator = "unknown"
		}
	}

	// Open the provenance row up front so a partial-failure run still
	// leaves an audit trail; we close it on the way out (success or
	// error). If the table itself is missing (migration 0020 hasn't
	// run yet) we proceed without a row rather than refusing to
	// backfill.
	runID := uuid.New()
	provenance := true
	if _, err := pool.Exec(ctx, `
		INSERT INTO crm.ktp_backfill_runs (id, triggered_by)
		VALUES ($1, $2)
	`, runID, operator); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: ktp_backfill_runs table missing (run migration 0020 to enable audit): %v\n",
			err)
		provenance = false
	}

	leadCount, leadErr := backfill(ctx, pool, sealer, "crm.leads")
	custCount, custErr := backfill(ctx, pool, sealer, "crm.customers")

	// First error wins for the row's error_message; both counts are
	// reported regardless.
	runErr := leadErr
	if runErr == nil {
		runErr = custErr
	}
	if provenance {
		closeRun(ctx, pool, runID, leadCount, custCount, runErr)
	}

	if runErr != nil {
		log.Fatalf("backfill failed: %v", runErr)
	}
	fmt.Printf("done.\n  crm.leads:     %d rows migrated\n  crm.customers: %d rows migrated\n",
		leadCount, custCount)
	if provenance {
		fmt.Printf("  run id:        %s\n", runID)
	}
}

// backfill encrypts every remaining plaintext NIK in `table`. Returns
// the count of migrated rows and the first error (if any). A
// post-migration-0018 run sees the `nik` column missing and treats
// that as "nothing to do."
func backfill(ctx context.Context, pool *pgxpool.Pool, s *cryptutil.Sealer, table string) (int, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT id, nik
		  FROM %s
		 WHERE nik IS NOT NULL AND nik <> ''
		   AND nik_encrypted IS NULL
	`, table))
	if err != nil {
		// Missing column = post-0018; nothing left to backfill.
		if strings.Contains(err.Error(), "does not exist") {
			return 0, nil
		}
		return 0, err
	}
	defer rows.Close()

	type pending struct {
		id  uuid.UUID
		nik string
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.nik); err != nil {
			return 0, err
		}
		batch = append(batch, p)
	}
	rows.Close()

	updated := 0
	for _, p := range batch {
		sealed, err := s.Seal(p.nik)
		if err != nil {
			return updated, fmt.Errorf("seal %s/%s: %w", table, p.id, err)
		}
		_, err = pool.Exec(ctx, fmt.Sprintf(`
			UPDATE %s SET nik_encrypted = $1, nik = NULL, updated_at = NOW()
			 WHERE id = $2
		`, table), sealed, p.id)
		if err != nil {
			return updated, fmt.Errorf("update %s/%s: %w", table, p.id, err)
		}
		updated++
	}
	return updated, nil
}

// closeRun stamps the provenance row with the completion time, counts,
// and (when present) the error message. Best-effort: a failure here is
// only emitted to stderr so the terminal still carries the outcome.
func closeRun(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, leads, customers int, runErr error) {
	var errMsg *string
	if runErr != nil {
		s := runErr.Error()
		errMsg = &s
	}
	_, err := pool.Exec(ctx, `
		UPDATE crm.ktp_backfill_runs
		   SET completed_at = NOW(),
		       leads_migrated = $2,
		       customers_migrated = $3,
		       error_message = $4
		 WHERE id = $1
	`, id, leads, customers, errMsg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to close provenance row %s: %v\n", id, err)
	}
}
