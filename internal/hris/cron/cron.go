// Package cron is the HRIS bounded context's cron runner.
//
// Two loops:
//   - HRISSyncDaily — every N hours (default 4), runs SyncService.RunFullSync
//   - EmployeeEventProcessor — every 5 minutes, drains the event queue
//
// The cron is designed to be safe to start with nil services — a missing
// SyncService or EventService just means the loop spins but no-ops.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/hris/usecase"
)

// Runner owns the two HRIS cron goroutines.
type Runner struct {
	log *slog.Logger

	sync   *usecase.SyncService
	events *usecase.EventService

	syncInterval  time.Duration
	drainInterval time.Duration
}

// New builds a Runner with defaults: sync every 4 hours, drain every
// 5 minutes. Override with WithSyncInterval / WithDrainInterval.
func New(log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		log:           log.With("component", "hris.cron"),
		syncInterval:  4 * time.Hour,
		drainInterval: 5 * time.Minute,
	}
}

// WithSyncService registers the sync service. nil → no-op loop.
func (r *Runner) WithSyncService(svc *usecase.SyncService) *Runner {
	r.sync = svc
	return r
}

// WithEventService registers the event service. nil → no-op loop.
func (r *Runner) WithEventService(svc *usecase.EventService) *Runner {
	r.events = svc
	return r
}

// WithSyncInterval overrides the daily-sync cadence.
func (r *Runner) WithSyncInterval(d time.Duration) *Runner {
	if d > 0 {
		r.syncInterval = d
	}
	return r
}

// WithDrainInterval overrides the event-drain cadence.
func (r *Runner) WithDrainInterval(d time.Duration) *Runner {
	if d > 0 {
		r.drainInterval = d
	}
	return r
}

// Start spawns the cron goroutines. The context drives shutdown — cancel
// it and the goroutines exit between ticks.
func (r *Runner) Start(ctx context.Context) {
	if r.sync != nil {
		go r.runSyncLoop(ctx)
	}
	if r.events != nil {
		go r.runDrainLoop(ctx)
	}
}

func (r *Runner) runSyncLoop(ctx context.Context) {
	// 90-second startup offset to avoid migration / boot races.
	if !sleep(ctx, 90*time.Second) {
		return
	}
	r.tickSync(ctx)
	t := time.NewTicker(r.syncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickSync(ctx)
		}
	}
}

func (r *Runner) tickSync(ctx context.Context) {
	if r.sync == nil {
		return
	}
	res, err := r.sync.RunFullSync(ctx)
	if err != nil {
		r.log.Warn("hris sync tick failed", "err", err)
		return
	}
	if res != nil && (res.EmployeesUpserted > 0 || res.EventsIngested > 0 || res.EventsProcessed > 0) {
		r.log.Info("hris sync tick",
			"employees_upserted", res.EmployeesUpserted,
			"events_ingested", res.EventsIngested,
			"events_processed", res.EventsProcessed)
	}
}

func (r *Runner) runDrainLoop(ctx context.Context) {
	if !sleep(ctx, 30*time.Second) {
		return
	}
	r.tickDrain(ctx)
	t := time.NewTicker(r.drainInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tickDrain(ctx)
		}
	}
}

func (r *Runner) tickDrain(ctx context.Context) {
	if r.events == nil {
		return
	}
	n, err := r.events.ProcessPending(ctx, 100)
	if err != nil {
		r.log.Warn("hris event drain tick failed", "err", err)
		return
	}
	if n > 0 {
		r.log.Info("hris event drain tick", "processed", n)
	}
}

// sleep is ctx-cancellable. Returns false when ctx is done.
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
