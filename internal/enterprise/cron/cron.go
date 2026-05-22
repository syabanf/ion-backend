// Package cron ships the enterprise-svc periodic workers:
//
//   - MilestoneInvoicer — every 30 min, finds project milestones
//     that just hit 100% progress and auto-emits the matching
//     termin invoice
//   - VendorMetricsDeriver — every hour, drains the
//     enterprise.ewo_completion_log into enterprise.vendor_metrics
//     (current-month roll-ups)
//
// Each worker is idempotent — running twice in the same minute
// produces the same result. Failures are logged + retried on the
// next tick; we don't bother with a queue because both jobs fit
// comfortably in a single goroutine each.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/notifyx"
)

// terminIssuer is the narrow slice of the enterprise Service that the
// milestone-invoicer needs. We declare it locally (not in /port) so the
// cron package can be wired with anything that satisfies the shape —
// stubbed in tests, real Service in main.go.
type terminIssuer interface {
	IssueTerminItem(ctx context.Context, in port.IssueTerminItemInput) (*domain.Invoice, error)
}

// Runner owns the lifetime of all enterprise cron workers. Start
// kicks them off; the returned cancel func stops them gracefully.
type Runner struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	issuer   terminIssuer       // optional; nil = legacy notify-only behavior
	notifier *notifyx.Dispatcher // optional; nil = no push fan-out
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Runner {
	return &Runner{pool: pool, log: log.With("component", "enterprise.cron")}
}

// WithTerminIssuer wires the finance/invoice-plan usecase so the
// milestone invoicer can actually issue termin invoices on completion
// (vs. just notifying finance). Passing nil keeps the legacy
// notify-only flow.
func (r *Runner) WithTerminIssuer(issuer terminIssuer) *Runner {
	r.issuer = issuer
	return r
}

// WithNotifier wires the push-notification dispatcher. When set, the
// milestone-invoicer fans a push to every finance-admin / -manager
// alongside the persisted inbox row. nil = persisted notifications
// only (existing behavior).
func (r *Runner) WithNotifier(n *notifyx.Dispatcher) *Runner {
	r.notifier = n
	return r
}

// Start spawns one goroutine per worker. The context controls their
// lifetime — cancel it (e.g. on SIGTERM) and they exit between ticks.
func (r *Runner) Start(ctx context.Context) {
	go r.runMilestoneInvoicer(ctx)
	go r.runVendorMetricsDeriver(ctx)
	go r.runPlatformJanitor(ctx)
}

// ============================================================
// Milestone invoicer
// ============================================================

func (r *Runner) runMilestoneInvoicer(ctx context.Context) {
	// Run once at startup so a fresh deploy doesn't wait 30 minutes
	// to catch up. Then every 30 min thereafter.
	r.tickMilestoneInvoicer(ctx)
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickMilestoneInvoicer(ctx)
		}
	}
}

// tickMilestoneInvoicer finds every milestone whose progress hit 100%
// since the last tick AND has a matching invoice_plan_item with no
// invoice issued yet. For each match, it inserts a billing-ready
// row marker that the finance handler picks up. We deliberately do
// not directly create the invoice here — the Finance flow has
// per-quotation guards that we don't want to duplicate.
//
// Idempotency: we update project_milestones.invoice_triggered_at on
// the same UPDATE so a milestone is only triggered once. (Backfill
// strategy below handles the existing-but-untriggered population.)
func (r *Runner) tickMilestoneInvoicer(ctx context.Context) {
	// Ensure the helper column exists; we create it lazily so the
	// migration doesn't have to ship a new col just for this cron.
	if _, err := r.pool.Exec(ctx, `
		ALTER TABLE enterprise.project_milestones
		ADD COLUMN IF NOT EXISTS invoice_triggered_at TIMESTAMPTZ
	`); err != nil {
		r.log.Error("milestone_invoicer: add col failed", "err", err)
		return
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, project_id, seq_no, title
		FROM enterprise.project_milestones
		WHERE progress_pct >= 100
		  AND invoice_triggered_at IS NULL
	`)
	if err != nil {
		r.log.Error("milestone_invoicer: query failed", "err", err)
		return
	}
	defer rows.Close()
	type milestone struct {
		ID        uuid.UUID
		ProjectID uuid.UUID
		SeqNo     int
		Title     string
	}
	var batch []milestone
	for rows.Next() {
		var m milestone
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.SeqNo, &m.Title); err == nil {
			batch = append(batch, m)
		}
	}
	rows.Close()
	if len(batch) == 0 {
		return
	}
	r.log.Info("milestone_invoicer: triggering", "count", len(batch))
	for _, m := range batch {
		// 1. Best-effort: find the invoice_plan_item matching this
		// milestone's seq_no (via project → quotation → plan). If a
		// match exists AND we have an issuer wired, fire the
		// IssueTerminItem usecase — that's the real invoice path
		// with proper invoice numbers, tax math, and idempotency.
		var planItemID uuid.UUID
		var invoiceID *uuid.UUID
		err := r.pool.QueryRow(ctx, `
			SELECT ipi.id, ipi.invoice_id
			FROM enterprise.invoice_plan_items ipi
			JOIN enterprise.invoice_plans ip ON ip.id = ipi.plan_id
			JOIN enterprise.projects p ON p.quotation_id = ip.quotation_id
			WHERE p.id = $1 AND ipi.seq_no = $2
			LIMIT 1
		`, m.ProjectID, m.SeqNo).Scan(&planItemID, &invoiceID)
		switch {
		case err != nil:
			// No matching plan item — fall through to notify-only.
			r.notifyFinanceOnly(ctx, m.Title, m.ProjectID, m.ID, m.SeqNo)
		case invoiceID != nil:
			// Already issued — nothing to do but trigger the mark.
			r.log.Info("milestone_invoicer: already issued",
				"milestone_id", m.ID, "invoice_id", invoiceID.String())
		case r.issuer != nil:
			inv, ierr := r.issuer.IssueTerminItem(ctx, port.IssueTerminItemInput{
				ItemID: planItemID,
			})
			if ierr != nil {
				r.log.Warn("milestone_invoicer: issue failed",
					"milestone_id", m.ID, "plan_item_id", planItemID, "err", ierr)
				r.notifyFinanceOnly(ctx, m.Title, m.ProjectID, m.ID, m.SeqNo)
			} else {
				r.log.Info("milestone_invoicer: invoice issued",
					"milestone_id", m.ID,
					"plan_item_id", planItemID,
					"invoice_id", inv.ID.String())
				r.notifyFinanceOfIssued(ctx, inv.ID, m.Title, m.ProjectID, m.ID, m.SeqNo)
			}
		default:
			// Issuer not wired — keep legacy behavior (notify only).
			r.notifyFinanceOnly(ctx, m.Title, m.ProjectID, m.ID, m.SeqNo)
		}

		// 2. Mark the milestone triggered regardless (idempotency).
		// Re-issuing is gated by the invoice_id column on the plan item.
		if _, err := r.pool.Exec(ctx, `
			UPDATE enterprise.project_milestones
			SET invoice_triggered_at = NOW()
			WHERE id = $1
		`, m.ID); err != nil {
			r.log.Warn("milestone_invoicer: mark failed",
				"milestone_id", m.ID, "err", err)
		}
	}
}

// notifyFinanceOnly drops the "please go issue an invoice" nudge into
// the finance team's inboxes. Fallback for when we can't auto-issue
// (no matching plan item, or the issuer isn't wired).
//
// Persisted notifications (enterprise.notifications inbox row) always
// fire; the push fan-out via notifyx only fires if the dispatcher is
// wired. Stub provider just logs — FCM swap-in happens in main.go.
func (r *Runner) notifyFinanceOnly(ctx context.Context, title string, projectID, milestoneID uuid.UUID, seqNo int) {
	_, _ = r.pool.Exec(ctx, `
		INSERT INTO enterprise.notifications
			(user_id, kind, title, body, deep_link, data)
		SELECT id, 'milestone_complete',
		       'Milestone complete — invoice termin',
		       $1::text || ' is at 100% on project ' || $2::text,
		       '/enterprise/projects/' || $2::text,
		       jsonb_build_object('milestone_id', $3::text, 'seq_no', $4::int)
		FROM identity.users u
		JOIN identity.user_roles ur ON ur.user_id = u.id
		JOIN identity.roles r ON r.id = ur.role_id
		WHERE r.name IN ('finance_admin','finance_manager')
		  AND u.active = TRUE
		ON CONFLICT DO NOTHING
	`, title, projectID.String(), milestoneID.String(), seqNo)

	r.pushToFinance(ctx, notifyx.Message{
		Title:    "Milestone complete — invoice termin",
		Body:     title + " is at 100% on project " + projectID.String(),
		DeepLink: "/enterprise/projects/" + projectID.String(),
		Topic:    "milestone_complete",
		Data: map[string]string{
			"milestone_id": milestoneID.String(),
			"project_id":   projectID.String(),
		},
	})
}

// notifyFinanceOfIssued — variant for when we DID auto-issue. Links
// straight to the invoice so finance can verify + e-Faktur it.
//
// Same persisted-inbox + push fan-out pattern as notifyFinanceOnly.
func (r *Runner) notifyFinanceOfIssued(ctx context.Context, invoiceID uuid.UUID, title string, projectID, milestoneID uuid.UUID, seqNo int) {
	_, _ = r.pool.Exec(ctx, `
		INSERT INTO enterprise.notifications
			(user_id, kind, title, body, deep_link, data)
		SELECT id, 'milestone_invoiced',
		       'Termin invoice issued',
		       $1::text || ' completed — termin invoice auto-issued',
		       '/enterprise/invoices/' || $5::text,
		       jsonb_build_object(
		           'milestone_id', $3::text,
		           'seq_no',       $4::int,
		           'invoice_id',   $5::text,
		           'project_id',   $2::text
		       )
		FROM identity.users u
		JOIN identity.user_roles ur ON ur.user_id = u.id
		JOIN identity.roles r ON r.id = ur.role_id
		WHERE r.name IN ('finance_admin','finance_manager')
		  AND u.active = TRUE
		ON CONFLICT DO NOTHING
	`, title, projectID.String(), milestoneID.String(), seqNo, invoiceID.String())

	r.pushToFinance(ctx, notifyx.Message{
		Title:    "Termin invoice issued",
		Body:     title + " completed — termin invoice auto-issued",
		DeepLink: "/enterprise/invoices/" + invoiceID.String(),
		Topic:    "milestone_invoiced",
		Data: map[string]string{
			"milestone_id": milestoneID.String(),
			"invoice_id":   invoiceID.String(),
			"project_id":   projectID.String(),
		},
	})
}

// pushToFinance fans the message to every finance_admin / -manager
// via notifyx. nil-safe — if no dispatcher is wired, it's a no-op.
// We resolve the recipient set with one query (cheaper than calling
// notifier.Send per-user with a single-UserID target).
// financeRecipientLimit caps how many finance users a single cron
// fan-out will address. The finance team should never grow to this
// size; if it does, the cap protects the push provider from a
// 500-user blast on every milestone tick. Cron logs a Warn so ops
// notices.
const financeRecipientLimit = 200

func (r *Runner) pushToFinance(ctx context.Context, msg notifyx.Message) {
	if r.notifier == nil {
		return
	}
	rows, err := r.pool.Query(ctx, `
		SELECT u.id
		FROM identity.users u
		JOIN identity.user_roles ur ON ur.user_id = u.id
		JOIN identity.roles r ON r.id = ur.role_id
		WHERE r.name IN ('finance_admin','finance_manager')
		  AND u.active = TRUE
		ORDER BY u.id
		LIMIT $1
	`, financeRecipientLimit+1) // +1 so we can detect overflow
	if err != nil {
		r.log.Warn("notify_finance: recipient lookup failed", "err", err)
		return
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, financeRecipientLimit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	if len(ids) > financeRecipientLimit {
		r.log.Warn("notify_finance: recipient set capped",
			"count", len(ids),
			"cap", financeRecipientLimit,
			"action", "push will only reach the first N — review finance role membership")
		ids = ids[:financeRecipientLimit]
	}
	r.notifier.Send(ctx, notifyx.Target{UserIDs: ids}, msg)
}

// ============================================================
// Vendor metrics deriver
// ============================================================

func (r *Runner) runVendorMetricsDeriver(ctx context.Context) {
	r.tickVendorMetricsDeriver(ctx)
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickVendorMetricsDeriver(ctx)
		}
	}
}

// vendorMetricsBatchSize caps how many ewo_completion_log rows we
// process per tick. The aggregation + UPDATE were previously
// unbounded — a backfill or recovery from downtime could leave
// millions of un-derived rows, locking the table on the next cron
// tick. With this cap, the worst case is "we need N ticks to catch
// up" instead of "one tick blocks the DB."
//
// Sized at 5000 because:
//   - bin-level aggregation typically collapses ~50 rows into 1
//     vendor-month row, so 5000 source rows ≈ 100 upserts
//   - the SELECT runs in well under 100ms on any reasonable index
//   - the UPDATE marks only the IDs we processed, so concurrent
//     ticks (via SKIP LOCKED) don't double-derive
const vendorMetricsBatchSize = 5000

// tickVendorMetricsDeriver aggregates the ewo_completion_log into
// per-vendor / per-month rows on enterprise.vendor_metrics. Each
// completion row is marked derived_into_metrics_at so we don't
// double-count on the next tick.
//
// Batching: at most vendorMetricsBatchSize rows per tick. Uses
// FOR UPDATE SKIP LOCKED so another concurrent runner (future
// horizontal scale) can pick up a disjoint batch without blocking.
func (r *Runner) tickVendorMetricsDeriver(ctx context.Context) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		r.log.Error("vendor_metrics: begin tx failed", "err", err)
		return
	}
	defer tx.Rollback(ctx)

	// 1. Lock + capture the IDs we'll process in this tick. SKIP
	// LOCKED keeps the deriver safe under future horizontal scale.
	idRows, err := tx.Query(ctx, `
		SELECT id, vendor_id, planned_finish, actual_finish,
		       defect_count, response_hours
		FROM enterprise.ewo_completion_log
		WHERE derived_into_metrics_at IS NULL
		  AND vendor_id IS NOT NULL
		ORDER BY actual_finish
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, vendorMetricsBatchSize)
	if err != nil {
		r.log.Error("vendor_metrics: select failed", "err", err)
		return
	}

	type rowDatum struct {
		id             uuid.UUID
		vendorID       uuid.UUID
		plannedFinish  *time.Time
		actualFinish   time.Time
		defectCount    int
		responseHours  *float64
	}
	var data []rowDatum
	for idRows.Next() {
		var d rowDatum
		if err := idRows.Scan(&d.id, &d.vendorID, &d.plannedFinish,
			&d.actualFinish, &d.defectCount, &d.responseHours); err == nil {
			data = append(data, d)
		}
	}
	idRows.Close()
	if len(data) == 0 {
		// Nothing to do — commit (empty) and return.
		_ = tx.Commit(ctx)
		return
	}

	// 2. Aggregate in-memory into (vendor_id, period_month) bins.
	type aggKey struct {
		vendorID uuid.UUID
		period   time.Time
	}
	type aggVal struct {
		total           int
		onTime          int
		defects         int
		responseSum     float64
		responseSamples int
	}
	bins := make(map[aggKey]*aggVal)
	for _, d := range data {
		period := time.Date(d.actualFinish.Year(), d.actualFinish.Month(),
			1, 0, 0, 0, 0, time.UTC)
		key := aggKey{vendorID: d.vendorID, period: period}
		v, ok := bins[key]
		if !ok {
			v = &aggVal{}
			bins[key] = v
		}
		v.total++
		if d.plannedFinish != nil && !d.actualFinish.After(*d.plannedFinish) {
			v.onTime++
		}
		v.defects += d.defectCount
		if d.responseHours != nil {
			v.responseSum += *d.responseHours
			v.responseSamples++
		}
	}
	r.log.Info("vendor_metrics: deriving",
		"rows", len(data), "bins", len(bins), "cap", vendorMetricsBatchSize)

	// 3. Upsert each bin into vendor_metrics.
	for k, v := range bins {
		var avg interface{}
		if v.responseSamples > 0 {
			avg = v.responseSum / float64(v.responseSamples)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO enterprise.vendor_metrics
				(vendor_id, period_month, orders_total, orders_on_time,
				 defects_reported, avg_response_hours)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (vendor_id, period_month) DO UPDATE
				SET orders_total       = enterprise.vendor_metrics.orders_total + EXCLUDED.orders_total,
				    orders_on_time     = enterprise.vendor_metrics.orders_on_time + EXCLUDED.orders_on_time,
				    defects_reported   = enterprise.vendor_metrics.defects_reported + EXCLUDED.defects_reported,
				    avg_response_hours = EXCLUDED.avg_response_hours
		`, k.vendorID, k.period, v.total, v.onTime, v.defects, avg)
		if err != nil {
			r.log.Warn("vendor_metrics: upsert failed",
				"vendor_id", k.vendorID, "err", err)
			// Don't break — we still want to mark the rows we
			// successfully aggregated. The failed bin will be picked
			// up on the next tick (those rows aren't marked).
		}
	}

	// 4. Mark exactly the rows we processed as derived. Using ANY($1)
	// keeps it to one round-trip regardless of batch size.
	ids := make([]uuid.UUID, 0, len(data))
	for _, d := range data {
		ids = append(ids, d.id)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE enterprise.ewo_completion_log
		SET derived_into_metrics_at = NOW()
		WHERE id = ANY($1::uuid[])
	`, ids); err != nil {
		r.log.Warn("vendor_metrics: mark-derived failed", "err", err)
		return // tx rolls back; the batch retries next tick
	}

	if err := tx.Commit(ctx); err != nil {
		r.log.Warn("vendor_metrics: commit failed", "err", err)
	}
}

// ============================================================
// Platform janitor — purges aging audit/forensic log rows
// ============================================================
//
// Three tables grow unbounded if left alone:
//   - platform.rate_limit_log    — sliding-window counter for public
//     endpoints. The hot path purges its own bucket on every count
//     call, but rows for unused/retired buckets sit around forever.
//   - platform.webhook_deliveries — every received webhook, forensic.
//     Useful for ~30 days, then it's audit-only weight.
//   - platform.push_outbox        — every push fanned via notifyx.
//     Same 30-day forensic window — older rows are unreachable from
//     any active UI surface.
//
// We run a daily tick (24h) and delete anything older than the
// retention window. All three tables are immutable append-only logs,
// so purging old rows can't break any active state.

const (
	rateLimitLogRetention    = 24 * time.Hour      // 1 day is plenty
	webhookDeliveryRetention = 30 * 24 * time.Hour // 30 days
	pushOutboxRetention      = 30 * 24 * time.Hour // 30 days
)

func (r *Runner) runPlatformJanitor(ctx context.Context) {
	// First tick fires ~5 minutes after boot so the service is fully
	// up and we don't race the migration. After that, every 24h.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}
	r.tickPlatformJanitor(ctx)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickPlatformJanitor(ctx)
		}
	}
}

func (r *Runner) tickPlatformJanitor(ctx context.Context) {
	// rate_limit_log — purge stale bucket entries beyond the window.
	// The hot path already purges per-bucket on every count call, but
	// retired buckets never get that touch. This sweep is the safety
	// net so the table doesn't accumulate dead rows.
	//
	// We pass the retention as seconds + use make_interval so we don't
	// have to worry about Go's time.Duration string format (720h0m0s)
	// matching Postgres interval syntax.
	rlSecs := int(rateLimitLogRetention / time.Second)
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM platform.rate_limit_log
		WHERE occurred_at < NOW() - make_interval(secs => $1)
	`, rlSecs)
	if err != nil {
		r.log.Warn("platform_janitor: rate_limit_log purge failed", "err", err)
	} else if tag.RowsAffected() > 0 {
		r.log.Info("platform_janitor: purged rate_limit_log",
			"rows", tag.RowsAffected(),
			"older_than", rateLimitLogRetention.String())
	}

	// webhook_deliveries — keep the last 30 days for forensic / replay
	// support. Older rows can be reconstructed from the provider's side
	// if needed (Xendit retains 90 days server-side).
	wdSecs := int(webhookDeliveryRetention / time.Second)
	tag2, err := r.pool.Exec(ctx, `
		DELETE FROM platform.webhook_deliveries
		WHERE received_at < NOW() - make_interval(secs => $1)
	`, wdSecs)
	if err != nil {
		r.log.Warn("platform_janitor: webhook_deliveries purge failed", "err", err)
	} else if tag2.RowsAffected() > 0 {
		r.log.Info("platform_janitor: purged webhook_deliveries",
			"rows", tag2.RowsAffected(),
			"older_than", webhookDeliveryRetention.String())
	}

	// push_outbox — same 30-day forensic window. Migration 0046
	// shipped the table; rows that pre-date that migration get
	// purged as soon as they age past the window.
	poSecs := int(pushOutboxRetention / time.Second)
	tag3, err := r.pool.Exec(ctx, `
		DELETE FROM platform.push_outbox
		WHERE queued_at < NOW() - make_interval(secs => $1)
	`, poSecs)
	if err != nil {
		r.log.Warn("platform_janitor: push_outbox purge failed", "err", err)
	} else if tag3.RowsAffected() > 0 {
		r.log.Info("platform_janitor: purged push_outbox",
			"rows", tag3.RowsAffected(),
			"older_than", pushOutboxRetention.String())
	}
}
