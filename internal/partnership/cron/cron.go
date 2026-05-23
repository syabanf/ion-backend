// Package cron holds the background tickers for the partnership
// bounded context.
//
// Wave 100 ships one entry: MonthlyComplianceEvaluator. The cron ticks
// daily and runs the evaluator for the most-recent fully-closed
// calendar month. Idempotent via the UNIQUE (reseller_account_id,
// period_year, period_month) constraint on
// partnership.compliance_evaluations — re-running on the same period
// is a no-op (Conflict on insert → logged + skipped, not an error).
//
// Same in-process tick model as warehouse-svc's Wave 88b cascade cron.
// Wave 100b can move this out via leader election when multi-replica
// deploys land.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/partnership/usecase"
)

// MonthlyComplianceEvaluator wraps the ComplianceService so the cron
// has a single dependency to inject. Holding the log + service here
// keeps the cmd-level wiring trivial.
type MonthlyComplianceEvaluator struct {
	svc *usecase.ComplianceService
	log *slog.Logger
}

func NewMonthlyComplianceEvaluator(svc *usecase.ComplianceService, log *slog.Logger) *MonthlyComplianceEvaluator {
	return &MonthlyComplianceEvaluator{svc: svc, log: log}
}

// ClosedMonth returns the (year, month) of the most-recent fully-closed
// calendar month relative to `now`. E.g. on 2026-05-23 it returns
// (2026, 4). Extracted so the test suite can pin it without time
// trickery.
func ClosedMonth(now time.Time) (int, int) {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	prev := first.AddDate(0, -1, 0)
	return prev.Year(), int(prev.Month())
}

// RunOnce runs the evaluator for the closed month relative to now.
// Used by the cron loop AND by the HTTP admin-trigger endpoint
// (POST /api/partnership/compliance/evaluate/{year}/{month} goes
// straight to ComplianceService.EvaluateMonth though, so RunOnce is
// strictly the "closed-month convenience" entry).
func (e *MonthlyComplianceEvaluator) RunOnce(ctx context.Context) {
	year, month := ClosedMonth(time.Now().UTC())
	summary, err := e.svc.EvaluateMonth(ctx, year, month)
	if err != nil {
		if e.log != nil {
			e.log.Error("compliance evaluator run failed",
				"year", year, "month", month, "err", err)
		}
		return
	}
	// Only log when something happened — silent ticks would flood the
	// logs in a healthy cluster (the cron runs daily, but the
	// evaluator is idempotent so most ticks are no-ops).
	if e.log != nil && summary.Evaluated > 0 {
		e.log.Info("compliance evaluator tick",
			"year", year, "month", month,
			"evaluated", summary.Evaluated,
			"ramp_skipped", summary.RampSkipped,
			"passed", summary.Passed,
			"breached", summary.Breached,
			"skipped_no_submission", summary.Skipped)
	}
}

// RunDaily ticks every 24h and calls RunOnce. Returns when ctx is
// cancelled (signal shutdown).
//
// We don't try to anchor at 02:00 local time — the underlying ticker
// fires every 24h starting at registration time. The idempotency on
// the (reseller, year, month) constraint makes the exact tick time
// uninteresting; the only requirement is "runs at least once a day"
// so a missed run rolls forward to the next tick.
func RunDaily(ctx context.Context, evaluator *MonthlyComplianceEvaluator) {
	const interval = 24 * time.Hour
	t := time.NewTicker(interval)
	defer t.Stop()
	// Immediate first tick so a freshly-started service evaluates
	// today's closed month without waiting 24h.
	evaluator.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			evaluator.RunOnce(ctx)
		}
	}
}
