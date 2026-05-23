// Wave 114 — Billing orchestration cron runner.
//
// One Runner, one goroutine per registered evaluator. Each goroutine
// owns its own ticker; cadences are independent because the
// evaluators don't share state (each writes to a distinct log table).
//
// Cadences (per the wave's design):
//
//   * Reminders          — every 30 min
//   * Late fee           — daily at 02:00 UTC (off-peak)
//   * Suspension         — daily at 03:00 UTC (off-peak; runs AFTER
//                          late-fee so the suspended-set already
//                          reflects today's late-fee bumps)
//   * Restore-on-paid    — every 5 min (most-reactive; a just-paid
//                          customer wants service back fast)
//   * Commission triggers— every 10 min
//
// Builder pattern matches enterprise/cron/cron.go. Every evaluator is
// optional: the runner spins only the goroutines whose Wither was
// called.

package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/usecase"
)

// reminderEvaluator is the narrow slice of OrchestrationService that
// the reminder goroutine calls. Declared locally so the cron package
// stays decoupled from the full usecase surface — tests can wire any
// shape matching this contract.
type reminderEvaluator interface {
	RunReminderTick(ctx context.Context) (int, error)
}

type lateFeeEvaluator interface {
	RunLateFeeTick(ctx context.Context) (int, error)
}

type suspensionEvaluator interface {
	RunSuspensionTick(ctx context.Context) (int, error)
}

type restoreEvaluator interface {
	RunRestoreTick(ctx context.Context) (int, error)
}

type commissionTriggerEvaluator interface {
	RunCommissionTriggerTick(ctx context.Context) (int, error)
}

// Runner owns the five Wave 114 cron goroutines. Builder methods
// register evaluators; Start kicks off the goroutines registered so
// far. The context controls their lifetime.
type Runner struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	reminder       reminderEvaluator
	lateFee        lateFeeEvaluator
	suspension     suspensionEvaluator
	restore        restoreEvaluator
	commissionTrig commissionTriggerEvaluator
}

// New constructs a runner. The pool is held for future health-check
// hooks; today only the log is used in the cron goroutines.
func New(pool *pgxpool.Pool, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{pool: pool, log: log.With("component", "billing.cron")}
}

// WithReminderEvaluator registers the reminder evaluator. Passing nil
// is a no-op (Start won't spin the goroutine).
func (r *Runner) WithReminderEvaluator(svc *usecase.OrchestrationService) *Runner {
	if svc == nil {
		return r
	}
	r.reminder = svc
	return r
}

func (r *Runner) WithLateFeeApplier(svc *usecase.OrchestrationService) *Runner {
	if svc == nil {
		return r
	}
	r.lateFee = svc
	return r
}

func (r *Runner) WithSuspensionEvaluator(svc *usecase.OrchestrationService) *Runner {
	if svc == nil {
		return r
	}
	r.suspension = svc
	return r
}

func (r *Runner) WithRadiusRestoreOnPaid(svc *usecase.OrchestrationService) *Runner {
	if svc == nil {
		return r
	}
	r.restore = svc
	return r
}

func (r *Runner) WithCommissionTriggerEvaluator(svc *usecase.OrchestrationService) *Runner {
	if svc == nil {
		return r
	}
	r.commissionTrig = svc
	return r
}

// Start spawns one goroutine per registered evaluator. The context
// drives shutdown — cancel it and the goroutines exit between ticks.
func (r *Runner) Start(ctx context.Context) {
	if r.reminder != nil {
		go r.runReminderLoop(ctx)
	}
	if r.lateFee != nil {
		go r.runLateFeeLoop(ctx)
	}
	if r.suspension != nil {
		go r.runSuspensionLoop(ctx)
	}
	if r.restore != nil {
		go r.runRestoreLoop(ctx)
	}
	if r.commissionTrig != nil {
		go r.runCommissionTriggerLoop(ctx)
	}
}

// =====================================================================
// Loops — one per cadence.
//
// Common pattern: small startup offset to dodge migration / boot
// races, then a fixed-interval ticker. The 30-min, 5-min, 10-min
// loops use simple time.NewTicker; the daily loops align to the
// target hour so we don't drift each restart.
// =====================================================================

func (r *Runner) runReminderLoop(ctx context.Context) {
	// 1-minute startup offset so we don't fire mid-migration.
	if !sleep(ctx, 1*time.Minute) {
		return
	}
	r.tickReminder(ctx)
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickReminder(ctx)
		}
	}
}

func (r *Runner) tickReminder(ctx context.Context) {
	if r.reminder == nil {
		return
	}
	n, err := r.reminder.RunReminderTick(ctx)
	if err != nil {
		r.log.Warn("reminder tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("reminder tick", "sent", n)
	}
}

func (r *Runner) runLateFeeLoop(ctx context.Context) {
	// Daily at 02:00 UTC. dailyDelay returns the duration until the
	// next 02:00; we tick once at boot too so a re-deploy doesn't
	// skip a day.
	if !sleep(ctx, 90*time.Second) {
		return
	}
	r.tickLateFee(ctx)
	for {
		d := dailyDelay(time.Now().UTC(), 2)
		if !sleep(ctx, d) {
			return
		}
		r.tickLateFee(ctx)
	}
}

func (r *Runner) tickLateFee(ctx context.Context) {
	if r.lateFee == nil {
		return
	}
	n, err := r.lateFee.RunLateFeeTick(ctx)
	if err != nil {
		r.log.Warn("late_fee tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("late_fee tick", "applied", n)
	}
}

func (r *Runner) runSuspensionLoop(ctx context.Context) {
	// Daily at 03:00 UTC — runs an hour after late-fee so the
	// suspended-set already includes today's late-fee bumps.
	if !sleep(ctx, 2*time.Minute) {
		return
	}
	r.tickSuspension(ctx)
	for {
		d := dailyDelay(time.Now().UTC(), 3)
		if !sleep(ctx, d) {
			return
		}
		r.tickSuspension(ctx)
	}
}

func (r *Runner) tickSuspension(ctx context.Context) {
	if r.suspension == nil {
		return
	}
	n, err := r.suspension.RunSuspensionTick(ctx)
	if err != nil {
		r.log.Warn("suspension tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("suspension tick", "emitted", n)
	}
}

func (r *Runner) runRestoreLoop(ctx context.Context) {
	// Most-reactive cadence: 5 min. A just-paid customer wants service
	// back fast. We still take a 30s startup offset so the boot
	// sequence is clean.
	if !sleep(ctx, 30*time.Second) {
		return
	}
	r.tickRestore(ctx)
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickRestore(ctx)
		}
	}
}

func (r *Runner) tickRestore(ctx context.Context) {
	if r.restore == nil {
		return
	}
	n, err := r.restore.RunRestoreTick(ctx)
	if err != nil {
		r.log.Warn("restore tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("restore tick", "restored", n)
	}
}

func (r *Runner) runCommissionTriggerLoop(ctx context.Context) {
	if !sleep(ctx, 45*time.Second) {
		return
	}
	r.tickCommissionTrigger(ctx)
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickCommissionTrigger(ctx)
		}
	}
}

func (r *Runner) tickCommissionTrigger(ctx context.Context) {
	if r.commissionTrig == nil {
		return
	}
	n, err := r.commissionTrig.RunCommissionTriggerTick(ctx)
	if err != nil {
		r.log.Warn("commission_trigger tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("commission_trigger tick", "queued", n)
	}
}

// =====================================================================
// helpers
// =====================================================================

// sleep blocks for `d` honoring ctx.Done(). Returns false when the
// context cancelled (caller should return).
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// dailyDelay returns the duration from `now` until the next
// occurrence of `targetHour` (UTC). If we're past the hour today,
// returns the offset to the same hour tomorrow.
func dailyDelay(now time.Time, targetHour int) time.Duration {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), targetHour, 0, 0, 0, time.UTC)
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate.Sub(now)
}
