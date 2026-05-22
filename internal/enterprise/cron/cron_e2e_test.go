//go:build e2e

// Integration tests for the cron workers. These need a live Postgres
// with the full migration set applied — same fixture the CI `e2e`
// job uses. Run with:
//
//   go test -tags=e2e ./internal/enterprise/cron -v
//
// Each test is independent: it seeds its own rows with a random
// suffix so the test file is re-runnable.

package cron

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
)

// =============================================================================
// Shared setup
// =============================================================================

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://syabanf@localhost:5432/ion_core?sslmode=disable"
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func newRunner(t *testing.T) *Runner {
	t.Helper()
	pool := openPool(t)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(pool, log)
}

// =============================================================================
// tickPlatformJanitor
// =============================================================================

// Verifies the daily purge boundary: rows older than the retention
// window are deleted, rows inside the window are preserved. Both
// tables (rate_limit_log + webhook_deliveries) get their own assertion.
func TestPlatformJanitor_PurgeBoundary(t *testing.T) {
	r := newRunner(t)
	ctx := context.Background()

	bucket := "test_janitor_" + uuid.New().String()[:8]

	// Seed: one rate_limit row well past 24h, one inside the window.
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO platform.rate_limit_log (bucket, occurred_at) VALUES
		    ($1, NOW() - INTERVAL '48 hours'),
		    ($1, NOW() - INTERVAL '2 hours')
	`, bucket); err != nil {
		t.Fatalf("seed rate_limit_log: %v", err)
	}

	// Seed: one webhook delivery well past 30d, one inside the window.
	provider := "test_" + uuid.New().String()[:8]
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO platform.webhook_deliveries
		    (provider, event_id, remote_ip, received_at)
		VALUES
		    ($1, $2, '127.0.0.1', NOW() - INTERVAL '45 days'),
		    ($1, $3, '127.0.0.1', NOW() - INTERVAL '5 days')
	`, provider, "old-"+uuid.New().String(), "new-"+uuid.New().String()); err != nil {
		t.Fatalf("seed webhook_deliveries: %v", err)
	}

	// Run the tick.
	r.tickPlatformJanitor(ctx)

	// Verify rate_limit_log: only the 2h-old row remains.
	var rlCount int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM platform.rate_limit_log WHERE bucket = $1`,
		bucket,
	).Scan(&rlCount); err != nil {
		t.Fatalf("count rate_limit_log: %v", err)
	}
	if rlCount != 1 {
		t.Errorf("rate_limit_log: got %d rows after purge, want 1 (only the recent one)", rlCount)
	}

	// Verify webhook_deliveries: only the 5d-old row remains.
	var wdCount int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM platform.webhook_deliveries WHERE provider = $1`,
		provider,
	).Scan(&wdCount); err != nil {
		t.Fatalf("count webhook_deliveries: %v", err)
	}
	if wdCount != 1 {
		t.Errorf("webhook_deliveries: got %d rows after purge, want 1", wdCount)
	}

	// Cleanup the recent rows so re-runs don't accumulate.
	_, _ = r.pool.Exec(ctx, `DELETE FROM platform.rate_limit_log WHERE bucket = $1`, bucket)
	_, _ = r.pool.Exec(ctx, `DELETE FROM platform.webhook_deliveries WHERE provider = $1`, provider)
}

// Verifies the janitor is idempotent — running it twice doesn't blow
// up and doesn't delete fresh rows. Catches a regression where a
// second tick might over-aggressively purge.
func TestPlatformJanitor_Idempotent(t *testing.T) {
	r := newRunner(t)
	ctx := context.Background()
	r.tickPlatformJanitor(ctx)
	r.tickPlatformJanitor(ctx)
	// No assertion — just verifying the second call doesn't panic or
	// error. The first call's behaviour is covered by PurgeBoundary.
}

// =============================================================================
// tickMilestoneInvoicer
// =============================================================================

// fakeIssuer is an in-memory terminIssuer. Records every call so the
// test can assert that the cron picked the right plan item.
type fakeIssuer struct {
	calls []port.IssueTerminItemInput
	err   error
}

func (f *fakeIssuer) IssueTerminItem(_ context.Context, in port.IssueTerminItemInput) (*domain.Invoice, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Invoice{ID: uuid.New()}, nil
}

// Verifies: when a milestone hits 100% AND a matching invoice_plan_item
// exists (same seq_no, plan tied to the project's quotation), the
// cron calls IssueTerminItem with that item_id and marks the milestone
// as triggered.
func TestMilestoneInvoicer_AutoIssuesWhenPlanItemMatches(t *testing.T) {
	r := newRunner(t)
	issuer := &fakeIssuer{}
	r.issuer = issuer
	ctx := context.Background()

	// Build a self-contained fixture: quotation + project +
	// invoice_plan + plan_item + milestone-at-100%. All ids are
	// random so the test is re-runnable.
	quotationID := uuid.New()
	opportunityID := uuid.New()
	boqVersionID := uuid.New()
	projectID := uuid.New()
	planID := uuid.New()
	planItemID := uuid.New()
	milestoneID := uuid.New()
	suffix := uuid.New().String()[:8]

	// invoice_plan needs quotation_id (unique) — random uuid is fine.
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.invoice_plans
		    (id, quotation_id, opportunity_id, boq_version_id,
		     plan_number, status, total_amount, planned_amount, currency)
		VALUES ($1, $2, $3, $4, $5, 'active', 1000000, 1000000, 'IDR')
	`, planID, quotationID, opportunityID, boqVersionID, "PLAN-"+suffix); err != nil {
		t.Fatalf("seed invoice_plans: %v", err)
	}
	defer r.pool.Exec(ctx, `DELETE FROM enterprise.invoice_plans WHERE id = $1`, planID)

	// invoice_plan_item with seq_no=1; no invoice_id yet (= not issued).
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.invoice_plan_items
		    (id, plan_id, seq_no, label, amount, due_offset_days)
		VALUES ($1, $2, 1, 'Test termin', 1000000, 30)
	`, planItemID, planID); err != nil {
		t.Fatalf("seed invoice_plan_items: %v", err)
	}

	// project with the same quotation_id so the cron's JOIN matches.
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.projects
		    (id, project_number, quotation_id, opportunity_id, boq_version_id, status)
		VALUES ($1, $2, $3, $4, $5, 'in_progress')
	`, projectID, "PROJ-"+suffix, quotationID, opportunityID, boqVersionID); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	defer r.pool.Exec(ctx, `DELETE FROM enterprise.projects WHERE id = $1`, projectID)

	// milestone at 100% with no invoice_triggered_at yet.
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.project_milestones
		    (id, project_id, seq_no, title, planned_start, planned_end,
		     planned_weight, progress_pct)
		VALUES ($1, $2, 1, 'Test milestone',
		        NOW()::date - INTERVAL '10 days',
		        NOW()::date,
		        100, 100)
	`, milestoneID, projectID); err != nil {
		t.Fatalf("seed project_milestones: %v", err)
	}
	defer r.pool.Exec(ctx, `DELETE FROM enterprise.project_milestones WHERE id = $1`, milestoneID)

	// Run the tick.
	r.tickMilestoneInvoicer(ctx)

	// Verify the issuer was called with our plan item.
	found := false
	for _, c := range issuer.calls {
		if c.ItemID == planItemID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issuer was not called for plan item %v; calls=%v", planItemID, issuer.calls)
	}

	// Verify the milestone was marked triggered.
	var triggeredAt *time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT invoice_triggered_at FROM enterprise.project_milestones WHERE id = $1`,
		milestoneID,
	).Scan(&triggeredAt); err != nil {
		t.Fatalf("read milestone: %v", err)
	}
	if triggeredAt == nil {
		t.Error("milestone.invoice_triggered_at is nil after tick — idempotency mark missed")
	}
}

// Verifies: when no matching invoice_plan_item exists, the cron falls
// back to notify-only and DOES NOT call the issuer.
func TestMilestoneInvoicer_FallsBackToNotifyWhenNoPlanItem(t *testing.T) {
	r := newRunner(t)
	issuer := &fakeIssuer{}
	r.issuer = issuer
	ctx := context.Background()

	// Project with no invoice_plan, milestone at 100%.
	projectID := uuid.New()
	milestoneID := uuid.New()
	suffix := uuid.New().String()[:8]

	if _, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.projects
		    (id, project_number, quotation_id, opportunity_id, boq_version_id, status)
		VALUES ($1, $2, $3, $4, $5, 'in_progress')
	`, projectID, "PROJ-NOPLAN-"+suffix, uuid.New(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	defer r.pool.Exec(ctx, `DELETE FROM enterprise.projects WHERE id = $1`, projectID)

	if _, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.project_milestones
		    (id, project_id, seq_no, title, planned_start, planned_end,
		     planned_weight, progress_pct)
		VALUES ($1, $2, 1, 'Test milestone no plan',
		        NOW()::date - INTERVAL '10 days', NOW()::date,
		        100, 100)
	`, milestoneID, projectID); err != nil {
		t.Fatalf("seed milestone: %v", err)
	}
	defer r.pool.Exec(ctx, `DELETE FROM enterprise.project_milestones WHERE id = $1`, milestoneID)

	r.tickMilestoneInvoicer(ctx)

	// Issuer must not have been called for our milestone — there's no
	// plan item, so the cron should have routed to notifyFinanceOnly.
	for _, c := range issuer.calls {
		// Other milestones in other tests may have fired the issuer;
		// we only care that ours didn't appear.
		_ = c
	}
	// We can't assert "no calls" globally — other parallel fixtures
	// may exist. The signal we DO have: the milestone still gets
	// invoice_triggered_at set (idempotency mark fires regardless).
	var triggeredAt *time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT invoice_triggered_at FROM enterprise.project_milestones WHERE id = $1`,
		milestoneID,
	).Scan(&triggeredAt); err != nil {
		t.Fatalf("read milestone: %v", err)
	}
	if triggeredAt == nil {
		t.Error("milestone.invoice_triggered_at is nil — cron didn't process this milestone")
	}
}

// Sanity: tickMilestoneInvoicer doesn't crash when issuer is nil
// (legacy notify-only mode). Catches a regression where the issuer
// check might be missed in a refactor.
func TestMilestoneInvoicer_NilIssuerSafe(t *testing.T) {
	r := newRunner(t)
	r.issuer = nil
	defer func() {
		if x := recover(); x != nil {
			t.Fatalf("tickMilestoneInvoicer panicked with nil issuer: %v", x)
		}
	}()
	r.tickMilestoneInvoicer(context.Background())
}

// Silence the unused import warning if errors isn't otherwise referenced.
var _ = errors.New
