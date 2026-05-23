// Package cron ships the operations module periodic workers.
//
// Wave 125 introduces BulkJobRunnerTick — every 5 minutes the worker
// scans `operations.bulk_jobs` for rows in 'pending' or 'running'
// state, picks one up, and dispatches it to the appropriate
// BulkExecutorService run method. Concurrency limit 1 per worker —
// one bulk job at a time so a runaway downstream (e.g. a flapping CRM
// schema) doesn't queue more pressure on top.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	"github.com/ion-core/backend/internal/operations/usecase"
)

// BulkJobRunnerTick polls + runs pending/running bulk jobs.
type BulkJobRunnerTick struct {
	jobs port.BulkJobRepository
	exec *usecase.BulkExecutorService
	log  *slog.Logger
	tick time.Duration
}

// NewBulkJobRunnerTick — default 5-minute cadence.
func NewBulkJobRunnerTick(jobs port.BulkJobRepository, exec *usecase.BulkExecutorService, log *slog.Logger) *BulkJobRunnerTick {
	if log == nil {
		log = slog.Default()
	}
	return &BulkJobRunnerTick{
		jobs: jobs,
		exec: exec,
		log:  log.With("worker", "operations_bulk_job_runner"),
		tick: 5 * time.Minute,
	}
}

// Start launches the polling goroutine. Idempotent — multiple Start
// calls produce multiple goroutines, so the caller is expected to call
// it once per process.
func (w *BulkJobRunnerTick) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *BulkJobRunnerTick) run(ctx context.Context) {
	// Short warm-up delay so we don't pile on right after a deploy.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	w.RunOnce(ctx)
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce picks up at most one runnable job per tick. Exported so e2e
// tests can drive the worker without waiting for a tick.
func (w *BulkJobRunnerTick) RunOnce(ctx context.Context) {
	jobs, err := w.jobs.ListRunnable(ctx, 1)
	if err != nil {
		w.log.Warn("list runnable bulk jobs failed", "err", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	j := jobs[0]
	if err := w.dispatch(ctx, j.Kind, j.ID); err != nil {
		w.log.Warn("bulk job dispatch failed",
			"job_id", j.ID, "kind", j.Kind, "err", err)
		return
	}
	w.log.Info("bulk job tick complete", "job_id", j.ID, "kind", j.Kind)
}

func (w *BulkJobRunnerTick) dispatch(ctx context.Context, kind domain.BulkJobKind, jobID uuid.UUID) error {
	switch kind {
	case domain.BulkJobPlanChange:
		_, err := w.exec.RunBulkPlanChange(ctx, jobID)
		return err
	case domain.BulkJobODPMigration:
		_, err := w.exec.RunBulkODPMigration(ctx, jobID)
		return err
	case domain.BulkJobWOCreation:
		_, err := w.exec.RunBulkWOCreation(ctx, jobID)
		return err
	default:
		// Other kinds (wo_cancellation, customer_segment_export) aren't
		// shipped in Wave 125; treat as no-op so the cron doesn't loop.
		return nil
	}
}
