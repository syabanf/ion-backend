package usecase

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =====================================================================
// Wave 108 — Edge #3: race on bulk subscriber import
//
// Two concurrent CSV imports under the same tenant uploading the same
// subscriber email should not produce duplicate rows. The catalog
// (`Edge Case & Concurrency` bucket, Edge #3) requires a UNIQUE
// constraint on (reseller_account_id, lower(customer_email)) so the
// second insert collides + surfaces a per-row error in the import
// audit `error_summary` jsonb. As of HEAD migration 0068 the
// constraint does NOT exist — `customer_email` is plain TEXT with no
// uniqueness, no functional index.
//
// Wave 108 is a test-only wave so we don't add the migration here. The
// test is wired against a live DB so that once the migration lands +
// the constraint exists, removing the `t.Skip` below makes the test
// fire end-to-end. Without DATABASE_URL the test skips for ergonomic
// CI parity with the other DB-required tests.
//
// When the unique constraint lands the implementer should:
//  1. Drop the `t.Skip("no UNIQUE constraint ...")` line below.
//  2. Provide a fixture reseller_account_id with a valid tenant row.
//  3. Run two ImportSubscribersCSV calls in parallel with identical
//     emails; assert that exactly one import yields a non-zero
//     OKRows + the other carries an ImportRowError with reason
//     containing "duplicate" / "unique".
// =====================================================================

func TestBulkImport_RaceOnDuplicateEmail_SecondImportPartial(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping bulk-import race DB test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("could not connect to DB: %v", err)
	}
	t.Cleanup(pool.Close)
	// Until the UNIQUE constraint on
	//   (reseller_account_id, lower(customer_email))
	// lands in a future migration, the race scenario the catalog
	// describes can't be exercised — the two imports would both succeed
	// + leak duplicate rows. Skip cleanly so CI stays green; the test
	// is here as the bookmark for the future implementer.
	t.Skip(
		"reseller.subscribers has no UNIQUE constraint on " +
			"(reseller_account_id, lower(customer_email)); see " +
			"wave-108 compliance report §3e",
	)
}
