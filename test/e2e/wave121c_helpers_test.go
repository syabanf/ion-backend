// Wave 121C — shared helpers for the new-context E2E suite.
//
// The Wave 121C tests target the five Phase 1B bounded contexts that
// landed in Waves 111-118 (payment, nocmon, netdev, hris, invoicesvc)
// plus the Wave 114 billing orchestration crons. Unlike the existing
// black-box tests that hit a running api-gateway on :8080, these tests
// wire each bounded context's usecase service in-process and exercise
// it against the live ion_p1b_smoke Postgres.
//
// Why this split:
//
//   - The five new contexts don't have a single integration target on
//     :8080 yet — payment-svc, nocmon-svc, netdev-svc, hris-svc,
//     invoice-svc are independent binaries. Running them all locally
//     under tests is fragile; wiring the usecase in-process is faster
//     and more deterministic.
//
//   - The Wave 114 crons are observed only via the persisted side-effect
//     rows they write. Calling the evaluator method directly is exactly
//     what we want — anything else (binary boot, signals, polling) just
//     adds flake.
//
// Each test file picks the surface it needs:
//
//   - payment_intent_lifecycle_e2e_test.go  → spins payment HTTP via
//     httptest.NewServer + real postgres adapters.
//   - noc_fault_lifecycle_e2e_test.go       → calls nocmon usecases
//     directly.
//   - netdev_lifecycle_e2e_test.go          → calls netdev usecases
//     directly.
//   - hris_resign_commission_cessation_e2e_test.go → calls hris +
//     billing.orchestration directly.
//   - invoice_snapshot_credit_note_e2e_test.go → calls invoicesvc
//     directly.
//   - billing_orchestration_cron_observability_e2e_test.go → calls the
//     OrchestrationService Run*Tick methods directly.
//
// Every test cleans up its scoped rows in t.Cleanup so re-runs don't
// fight previous data. We tag uniqueness via uuid.New() at top of each
// test so cleanup is a single DELETE-by-id family.
//
//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// w121cDBOnce + w121cPool give us a process-wide singleton pgxpool so
// every test file shares one connection pool. pgx is goroutine-safe and
// reuses idle conns; one shared pool is much faster than per-test ones.
var (
	w121cDBOnce sync.Once
	w121cPool   *pgxpool.Pool
	w121cInitErr error
)

// w121cDB returns the shared pgxpool for the Wave 121C E2E suite. The
// pool reads DATABASE_URL from env; the acceptance gate sets it to
// `postgres://syabanf@localhost:5432/ion_p1b_smoke?sslmode=disable`.
//
// On the first call we open the pool + ping. Subsequent calls return
// the same pool. The pool stays open for the lifetime of the test
// process; pgxpool's idle-conn pruning handles cleanup.
func w121cDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	w121cDBOnce.Do(func() {
		dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
		if dbURL == "" {
			dbURL = "postgres://syabanf@localhost:5432/ion_p1b_smoke?sslmode=disable"
		}
		cfg, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			w121cInitErr = err
			return
		}
		// Keep the pool small — the suite never runs more than a few
		// queries in flight, and a tighter pool surfaces leaks faster.
		cfg.MaxConns = 8
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			w121cInitErr = err
			return
		}
		if err := pool.Ping(ctx); err != nil {
			w121cInitErr = err
			return
		}
		w121cPool = pool
	})
	if w121cInitErr != nil {
		t.Skipf("Wave 121C: cannot reach DATABASE_URL — %v", w121cInitErr)
	}
	if w121cPool == nil {
		t.Skip("Wave 121C: pgxpool not initialised")
	}
	return w121cPool
}

// w121cSkipIfMissingTable bails out with t.Skip when a required table
// isn't present in the target DB. Useful for tests that depend on a
// migration that may not have been applied locally — we'd rather skip
// loudly than fail in a noisy way the engineer has to debug.
func w121cSkipIfMissingTable(t *testing.T, pool *pgxpool.Pool, schemaTable string) {
	t.Helper()
	parts := strings.SplitN(schemaTable, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("w121cSkipIfMissingTable: want schema.table, got %q", schemaTable)
	}
	var ok bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
		   SELECT 1 FROM information_schema.tables
		   WHERE table_schema = $1 AND table_name = $2
		 )`, parts[0], parts[1]).Scan(&ok)
	if err != nil {
		t.Skipf("Wave 121C: cannot probe %s: %v", schemaTable, err)
	}
	if !ok {
		t.Skipf("Wave 121C: required table %s missing (migration not applied?)", schemaTable)
	}
}

// w121cCleanup deletes a scoped set of rows from `schema.table` keyed
// by a specific column = value. Wrapped in a t.Cleanup-friendly helper
// so test bodies stay short. Errors only log — cleanup failures must
// never mask the original test failure.
func w121cCleanup(pool *pgxpool.Pool, schemaTable, keyCol, keyVal string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx,
			`DELETE FROM `+schemaTable+` WHERE `+keyCol+` = $1`, keyVal)
	}
}

// w121cExec runs a one-shot Exec and t.Fatals on error. Wraps the
// boilerplate around timeout + error formatting.
func w121cExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("w121cExec: %v\nSQL: %s", err, sql)
	}
}
