// Wave 95b / Wave 101 — InternalTransaction reconciliation cron.
//
// Wave 95 introduced a second write path for the sub-company revenue
// ledger: AcceptIntercompanyPO now records a row with
// source_event='ic_po_accept' alongside the legacy
// recordInternalTransactionsOnApproval which writes
// source_event='boq_approval'. The unique index on boq_line_id
// prevents the SAME line from getting two rows from the same source,
// but doesn't help when the two sources race for the same line — and
// it doesn't surface DIVERGENCE (different recognized_amount /
// margin_amount between the two views).
//
// This cron runs daily and:
//
//  1. Finds every boq_line_id that has rows from BOTH sources with
//     different recognized amounts.
//  2. Emits a `reconciliation.divergence_detected` audit row per
//     cluster so the finance team can see "the two ledgers disagree
//     about this line — investigate."
//  3. Marks the legacy boq_approval row as superseded_at=NOW() so
//     reporting queries that filter out superseded rows naturally
//     prefer the canonical ic_po_accept view.
//
// Idempotent: runs forever without growing weirdness. Already-
// superseded rows are skipped on subsequent ticks.
package cron

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/audit"
)

// InternalTransactionReconciler is the daily reconciler. Self-
// contained; owns its own ticker so the wider Runner just needs to
// kick off Start().
type InternalTransactionReconciler struct {
	pool   *pgxpool.Pool
	audit  audit.Writer
	log    *slog.Logger
}

// NewInternalTransactionReconciler constructs the reconciler. The
// audit writer is required so the divergence signal lands somewhere
// permanent; if the caller doesn't have one wired, pass audit.Nop{}.
func NewInternalTransactionReconciler(
	pool *pgxpool.Pool,
	auditWriter audit.Writer,
	log *slog.Logger,
) *InternalTransactionReconciler {
	if auditWriter == nil {
		auditWriter = audit.Nop{}
	}
	return &InternalTransactionReconciler{
		pool:  pool,
		audit: auditWriter,
		log:   log.With("component", "enterprise.cron.internal_tx_reconciler"),
	}
}

// Start spawns the daily ticker. First tick runs ~5 minutes after
// boot so the service is warm (mirrors platform_janitor's cadence).
// The returned goroutine exits when ctx is cancelled.
func (r *InternalTransactionReconciler) Start(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
		r.RunOnce(ctx)
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.RunOnce(ctx)
			}
		}
	}()
}

// divergentLine is the cursor shape returned by the detection query.
type divergentLine struct {
	BOQLineID         uuid.UUID
	LegacyID          uuid.UUID  // boq_approval row id
	CanonicalID       uuid.UUID  // ic_po_accept row id
	LegacyAmount      float64
	CanonicalAmount   float64
	Delta             float64
}

// RunOnce executes a single reconciliation pass. Exposed for tests +
// for an admin "kick it now" surface — the daily ticker calls it on a
// schedule.
func (r *InternalTransactionReconciler) RunOnce(ctx context.Context) {
	rows, err := r.pool.Query(ctx, `
		WITH paired AS (
		    SELECT
		        legacy.boq_line_id           AS boq_line_id,
		        legacy.id                    AS legacy_id,
		        canonical.id                 AS canonical_id,
		        legacy.sell_amount           AS legacy_amount,
		        canonical.sell_amount        AS canonical_amount
		    FROM enterprise.internal_transactions AS legacy
		    JOIN enterprise.internal_transactions AS canonical
		      ON canonical.boq_line_id = legacy.boq_line_id
		     AND canonical.id <> legacy.id
		    WHERE legacy.source_event = 'boq_approval'
		      AND canonical.source_event = 'ic_po_accept'
		      AND legacy.superseded_at IS NULL
		)
		SELECT boq_line_id, legacy_id, canonical_id,
		       legacy_amount, canonical_amount
		FROM paired
		WHERE ABS(legacy_amount - canonical_amount) > 0.005
	`)
	if err != nil {
		r.log.Error("query failed", "err", err)
		return
	}
	defer rows.Close()

	var clusters []divergentLine
	for rows.Next() {
		var d divergentLine
		if err := rows.Scan(
			&d.BOQLineID, &d.LegacyID, &d.CanonicalID,
			&d.LegacyAmount, &d.CanonicalAmount,
		); err != nil {
			r.log.Warn("scan failed", "err", err)
			continue
		}
		d.Delta = d.LegacyAmount - d.CanonicalAmount
		clusters = append(clusters, d)
	}
	if len(clusters) == 0 {
		// Even on a clean tick, also pick up the no-divergence-but-
		// legacy-still-not-marked-superseded case: when the IC-PO
		// accept row matches the legacy row exactly, we still want
		// to flag the legacy as superseded so reporting queries
		// can ignore it cleanly.
		r.markCleanLegacyAsSuperseded(ctx)
		return
	}

	r.log.Info("divergence clusters detected", "count", len(clusters))

	for _, c := range clusters {
		// 1. Emit audit signal so finance sees this. Same Entry
		// shape as elsewhere — Reason captures the structured detail.
		audit.SafeWrite(ctx, r.audit, audit.Entry{
			Module:       "enterprise",
			RecordType:   "enterprise.internal_transaction",
			RecordID:     c.BOQLineID.String(),
			FieldChanged: "recognized_amount",
			Before:       formatAmount(c.LegacyAmount),
			After:        formatAmount(c.CanonicalAmount),
			Reason: "boq_line_id=" + c.BOQLineID.String() +
				" sources=BOQ_APPROVAL,IC_PO_ACCEPT delta=" +
				formatAmount(c.Delta),
		})

		// 2. keepCanonical — prefer the IC-PO-sourced row by
		// marking the legacy boq_approval row superseded_at=NOW().
		// We don't delete; the historical row stays put for the
		// audit trail.
		if _, err := r.pool.Exec(ctx, `
			UPDATE enterprise.internal_transactions
			SET superseded_at = NOW()
			WHERE id = $1 AND superseded_at IS NULL
		`, c.LegacyID); err != nil {
			r.log.Warn("supersede update failed",
				"legacy_id", c.LegacyID, "err", err)
		}
	}

	// Also sweep the non-divergent case so reporting consistently
	// filters out legacy rows once the canonical row exists.
	r.markCleanLegacyAsSuperseded(ctx)
}

// markCleanLegacyAsSuperseded handles the simpler case: a legacy
// boq_approval row and a canonical ic_po_accept row exist for the same
// boq_line_id with matching amounts. We still want to mark the legacy
// row superseded so dashboards and exports filtering on
// superseded_at IS NULL show only the canonical view.
func (r *InternalTransactionReconciler) markCleanLegacyAsSuperseded(ctx context.Context) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.internal_transactions AS legacy
		SET superseded_at = NOW()
		FROM enterprise.internal_transactions AS canonical
		WHERE legacy.boq_line_id = canonical.boq_line_id
		  AND legacy.id <> canonical.id
		  AND legacy.source_event = 'boq_approval'
		  AND canonical.source_event = 'ic_po_accept'
		  AND legacy.superseded_at IS NULL
	`)
	if err != nil {
		r.log.Warn("clean-legacy supersede failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		r.log.Info("legacy rows superseded by canonical",
			"rows", tag.RowsAffected())
	}
}

// formatAmount renders a NUMERIC(18,2)-shaped float as a fixed-
// precision string. Used in the audit reason so the value reads
// cleanly in the admin UI ("delta=15000000.00") rather than scientific
// notation.
func formatAmount(v float64) string {
	return fmt.Sprintf("%.2f", v)
}
