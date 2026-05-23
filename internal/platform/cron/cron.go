// Wave 116 — Platform cron runner.
//
// Currently single-purpose: a nightly sweep over every published schema
// that re-runs the content validator and writes results to
// platform.schema_validation_results. Ops gets an alert (audit log row)
// per invalid result so a stale schema doesn't silently rot in
// production.
//
// Modelled on internal/billing/cron's Runner — builder pattern, one
// goroutine per registered tick, context-driven shutdown. The platform
// runner stays small on purpose; if future waves add more platform
// crons (sweep webhook deliveries, prune validation_results, etc.)
// they slot in as additional Wither methods.

package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// validationSweeper is the narrow interface the nightly goroutine
// calls. Implemented by *platform/usecase.Service via
// ValidateAllPublishedSchemas.
type validationSweeper interface {
	ValidateAllPublishedSchemas(ctx context.Context) (invalid int, total int, err error)
}

// Runner owns the platform cron goroutines.
type Runner struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	sweep     validationSweeper
	sweepHour int // UTC hour for the sweep (default 4)
}

// New constructs a runner. pool is held so future health checks have
// something to query; today only the log is wired in the goroutines.
func New(pool *pgxpool.Pool, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		pool:      pool,
		log:       log.With("component", "platform.cron"),
		sweepHour: 4,
	}
}

// WithValidationSweeper registers the schema-validation sweeper. Nil is
// a no-op (Start won't spin the goroutine).
func (r *Runner) WithValidationSweeper(s validationSweeper) *Runner {
	if s == nil {
		return r
	}
	r.sweep = s
	return r
}

// WithSweepHour overrides the default 04:00 UTC sweep time. Useful in
// tests; production should stay at 04:00 (well into off-peak).
func (r *Runner) WithSweepHour(h int) *Runner {
	if h < 0 || h > 23 {
		return r
	}
	r.sweepHour = h
	return r
}

// Start spawns one goroutine per registered tick. Cancel the context
// to shut down.
func (r *Runner) Start(ctx context.Context) {
	if r.sweep != nil {
		go r.runSweepLoop(ctx)
	}
}

// runSweepLoop runs the validator sweep daily at sweepHour UTC. Boot
// kicks one immediate sweep (after a 2-min offset to dodge migration
// races) so a re-deploy doesn't skip a day.
func (r *Runner) runSweepLoop(ctx context.Context) {
	if !sleep(ctx, 2*time.Minute) {
		return
	}
	r.tickSweep(ctx)
	for {
		d := dailyDelay(time.Now().UTC(), r.sweepHour)
		if !sleep(ctx, d) {
			return
		}
		r.tickSweep(ctx)
	}
}

func (r *Runner) tickSweep(ctx context.Context) {
	if r.sweep == nil {
		return
	}
	invalid, total, err := r.sweep.ValidateAllPublishedSchemas(ctx)
	if err != nil {
		r.log.Warn("schema_validation sweep failed", "err", err)
		return
	}
	if invalid > 0 {
		r.log.Warn("schema_validation sweep found invalid schemas",
			"invalid", invalid, "total", total)
	} else {
		r.log.Info("schema_validation sweep clean", "total", total)
	}
}

// =====================================================================
// Helpers — copy of the billing/cron primitives (small enough that
// inlining beats import cycles).
// =====================================================================

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

func dailyDelay(now time.Time, targetHour int) time.Duration {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), targetHour, 0, 0, 0, time.UTC)
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate.Sub(now)
}
