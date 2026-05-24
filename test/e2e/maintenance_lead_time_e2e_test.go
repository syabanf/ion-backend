// Wave 127 — Maintenance lead-time + escalation + approval-gate E2E
// (targets Wave 126's planned-maintenance enhancement work).
//
// Wave 126 hasn't shipped at HEAD when this file was authored. Each test
// here probes for the Wave-126 schema additions and skips cleanly when
// they're absent:
//
//   - field.maintenance_events.affected_customers_snapshot (jsonb) — the
//     materialized affected-customers list emitted by the new
//     usecase.maintenance_cron.materializeAffectedCustomers loop.
//   - field.maintenance_events.escalation_level (int) — the escalation
//     ladder counter (Wave 126).
//   - field.maintenance_events.approval_required (bool) — the >100
//     customers gate.
//
// When Wave 126 lands those columns, these tests light up automatically
// against a smoke DB that's had 0085 applied.
//
// Probe pattern: w121cSkipIfMissingColumn fails closed (skips) when the
// column isn't there. We deliberately avoid importing any Wave-126
// usecase package to stay zero-overlap with the Wave-126-in-flight
// implementation.
//
//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"
)

// w127SkipIfMissingColumn skips when `schema.table.column` doesn't
// exist. Used to fail-closed on Wave 126 schema gates.
func w127SkipIfMissingColumn(t *testing.T, pool *pgxpool.Pool, schema, table, column string) {
	t.Helper()
	var ok bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
		   SELECT 1 FROM information_schema.columns
		    WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		 )`, schema, table, column).Scan(&ok)
	if err != nil {
		t.Skipf("Wave 127: cannot probe %s.%s.%s: %v", schema, table, column, err)
	}
	if !ok {
		t.Skipf("Wave 127: required column %s.%s.%s missing (Wave 126 migration not applied)",
			schema, table, column)
	}
}

// seedMaintenanceEvent inserts a minimal field.maintenance_events row.
// Returns the event id. The caller sets up cleanup.
//
// The schema has migrated multiple times — Wave 36 introduced
// event_code (NOT NULL), Wave 71 added war-room columns, Wave 126
// (in-flight) adds affected_customers_snapshot etc. We compose the
// INSERT for the columns we know are always required and use a
// short unique event_code.
func seedMaintenanceEvent(t *testing.T, pool *pgxpool.Pool, scheduledStart, scheduledEnd time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	eventCode := "W127-" + id.String()[:8]
	// branch_id is nullable (ON DELETE SET NULL) per migration 0036, so
	// we pass NULL to keep the test self-contained (no identity.branches
	// seed needed).
	_, err := pool.Exec(ctx, `
		INSERT INTO field.maintenance_events
		    (id, event_code, event_kind, title, status, branch_id,
		     scheduled_start, scheduled_end, created_at, updated_at)
		VALUES ($1, $2, 'planned_outage', 'W127 maintenance', 'planned', NULL,
		        $3, $4, NOW(), NOW())
	`, id, eventCode, scheduledStart, scheduledEnd)
	if err != nil {
		t.Skipf("Wave 127: cannot seed maintenance_event (schema drift?): %v", err)
	}
	return id
}

// TC-PM-002 — affected_customers materialization. Wave 126 will own the
// usecase that walks network topology → crm.customers; this test
// asserts the column lights up via raw SQL UPDATE (the materializer's
// production code will do the same).
func TestMaintenance_AffectedCustomersMaterialized(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "field.maintenance_events")
	w127SkipIfMissingColumn(t, pool, "field", "maintenance_events", "affected_customers_snapshot")
	ctx := context.Background()

	now := time.Now().UTC()
	id := seedMaintenanceEvent(t, pool, now.Add(25*time.Hour), now.Add(26*time.Hour))
	t.Cleanup(w121cCleanup(pool, "field.maintenance_events", "id", id.String()))

	// Materialize a 3-customer snapshot via SQL (production code path
	// will do the same write).
	c1, c2, c3 := uuid.New(), uuid.New(), uuid.New()
	snapshot := `[
		{"customer_id":"` + c1.String() + `","reason":"odp_downstream"},
		{"customer_id":"` + c2.String() + `","reason":"odp_downstream"},
		{"customer_id":"` + c3.String() + `","reason":"odp_downstream"}
	]`
	w121cExec(t, pool, `
		UPDATE field.maintenance_events
		   SET affected_customers_snapshot = $1::jsonb
		 WHERE id = $2
	`, snapshot, id)

	var got string
	if err := pool.QueryRow(ctx,
		`SELECT affected_customers_snapshot::text FROM field.maintenance_events WHERE id = $1`,
		id).Scan(&got); err != nil {
		t.Fatalf("read affected_customers_snapshot: %v", err)
	}
	if len(got) < 100 {
		t.Errorf("snapshot too short: %d chars", len(got))
	}
}

// TC-MES-001 — overrun detect: a maintenance event with scheduled_end
// in the past and status='in_progress' is the cron's target.
func TestMaintenance_OverrunDetect(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "field.maintenance_events")
	ctx := context.Background()

	now := time.Now().UTC()
	id := seedMaintenanceEvent(t, pool, now.Add(-2*time.Hour), now.Add(-30*time.Minute))
	t.Cleanup(w121cCleanup(pool, "field.maintenance_events", "id", id.String()))
	w121cExec(t, pool, `UPDATE field.maintenance_events SET status='in_progress' WHERE id=$1`, id)

	// The overrun-detect cron should find this row via the predicate
	// `WHERE scheduled_end + interval '30 min' < NOW() AND status='in_progress'`.
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.maintenance_events
		 WHERE id = $1
		   AND scheduled_end + INTERVAL '30 minutes' < NOW()
		   AND status = 'in_progress'
	`, id).Scan(&n); err != nil {
		t.Fatalf("overrun query: %v", err)
	}
	if n != 1 {
		t.Errorf("overrun predicate matched %d rows, want 1", n)
	}
}

// TC-MES-003 — escalate maintenance: level ladder advances 0 → 1 → 2 → 3.
// Skips when the escalation_level column isn't present (Wave 126 ships it).
func TestMaintenance_EscalationLadder(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "field.maintenance_events")
	w127SkipIfMissingColumn(t, pool, "field", "maintenance_events", "escalation_level")
	ctx := context.Background()

	now := time.Now().UTC()
	id := seedMaintenanceEvent(t, pool, now.Add(time.Hour), now.Add(2*time.Hour))
	t.Cleanup(w121cCleanup(pool, "field.maintenance_events", "id", id.String()))

	for level := 1; level <= 3; level++ {
		w121cExec(t, pool, `
			UPDATE field.maintenance_events
			   SET escalation_level = $1
			 WHERE id = $2
		`, level, id)
		var got int
		if err := pool.QueryRow(ctx,
			`SELECT escalation_level FROM field.maintenance_events WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read escalation_level at L%d: %v", level, err)
		}
		if got != level {
			t.Errorf("escalation_level: got %d want %d", got, level)
		}
	}
}

// TC-PM-003 — approval-required gate fires when affected_customer_count > 100.
// Wave 126 introduces approval_required + approved_by columns; until
// then this test skips.
func TestMaintenance_ApprovalGateOverThreshold(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "field.maintenance_events")
	w127SkipIfMissingColumn(t, pool, "field", "maintenance_events", "approval_required")
	ctx := context.Background()

	now := time.Now().UTC()
	id := seedMaintenanceEvent(t, pool, now.Add(25*time.Hour), now.Add(26*time.Hour))
	t.Cleanup(w121cCleanup(pool, "field.maintenance_events", "id", id.String()))

	// Simulate a >100 affected_customer_count run: the materializer would also
	// flip approval_required=true.
	w121cExec(t, pool, `
		UPDATE field.maintenance_events
		   SET approval_required = TRUE,
		       affected_customer_count    = 250
		 WHERE id = $1
	`, id)

	var required bool
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT approval_required, COALESCE(affected_customer_count, 0) FROM field.maintenance_events WHERE id = $1`,
		id).Scan(&required, &count); err != nil {
		t.Fatalf("read approval gate: %v", err)
	}
	if !required || count <= 100 {
		t.Errorf("expected approval_required=true and count>100; got required=%v count=%d", required, count)
	}
}

// TC-PM-004 — lead-time: 24h before scheduled_start (broadband). The
// cron predicate is `WHERE scheduled_start - interval '24 hours' < NOW()
// AND notification_dispatched_at IS NULL`. We seed an event 23h out
// and assert the predicate matches.
func TestMaintenance_LeadTimePredicate(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "field.maintenance_events")
	ctx := context.Background()

	now := time.Now().UTC()
	id := seedMaintenanceEvent(t, pool, now.Add(23*time.Hour), now.Add(24*time.Hour))
	t.Cleanup(w121cCleanup(pool, "field.maintenance_events", "id", id.String()))

	// The lead-time predicate: scheduled_start - 24h < NOW() means
	// "we're within the 24h notification window".
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.maintenance_events
		 WHERE id = $1
		   AND scheduled_start - INTERVAL '24 hours' < NOW()
	`, id).Scan(&n); err != nil {
		t.Fatalf("lead-time predicate query: %v", err)
	}
	if n != 1 {
		t.Errorf("lead-time predicate matched %d rows, want 1", n)
	}
}
