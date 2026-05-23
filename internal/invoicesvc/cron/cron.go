// Package cron ships the invoicesvc periodic workers.
//
// Wave 115 ships two workers:
//
//   - BulkJobRunner — polls invoicesvc.bulk_generation_jobs in
//     'pending' state every minute, picks up the next one, and runs
//     it. Concurrency-limited: at most one job at a time per process
//     so the InvoiceGenerator load doesn't stampede.
//
//   - SnapshotBackfillScan — once a day, scans billing.invoices
//     issued in the last 24h that don't yet have a snapshot row, and
//     fires CreateSnapshot for each. Idempotent via the UNIQUE
//     (invoice_id, snapshotted_at) constraint on the snapshots table.
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/ion-core/backend/internal/invoicesvc/port"
)

// BulkJobRunner polls and runs pending bulk-generation jobs.
type BulkJobRunner struct {
	bulk    port.BulkUseCase
	jobRepo port.BulkJobRepository
	log     *slog.Logger
	tick    time.Duration
}

func NewBulkJobRunner(bulk port.BulkUseCase, jobs port.BulkJobRepository, log *slog.Logger) *BulkJobRunner {
	return &BulkJobRunner{
		bulk:    bulk,
		jobRepo: jobs,
		log:     log.With("worker", "invoicesvc_bulk_job_runner"),
		tick:    1 * time.Minute,
	}
}

func (w *BulkJobRunner) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *BulkJobRunner) run(ctx context.Context) {
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

// RunOnce picks up at most one pending job per tick. Idempotent: if
// another process picked it up first, the second RunJob will see
// non-pending status and return the terminal snapshot.
func (w *BulkJobRunner) RunOnce(ctx context.Context) {
	jobs, err := w.jobRepo.ListPending(ctx, 1)
	if err != nil {
		w.log.Warn("list pending bulk jobs failed", "err", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	for _, j := range jobs {
		job, err := w.bulk.RunJob(ctx, j.ID)
		if err != nil {
			w.log.Warn("run bulk job failed", "job_id", j.ID, "err", err)
			continue
		}
		w.log.Info("bulk job run", "job_id", job.ID, "status", string(job.Status),
			"generated", job.TotalGenerated, "failed", job.TotalFailed)
	}
}

// SnapshotBackfillScan generates snapshots for invoices that should
// have one but don't (race / migration / hook outage).
type SnapshotBackfillScan struct {
	snapshots port.SnapshotUseCase
	reader    port.InvoiceReader
	log       *slog.Logger
	tick      time.Duration
}

func NewSnapshotBackfillScan(snapshots port.SnapshotUseCase, reader port.InvoiceReader, log *slog.Logger) *SnapshotBackfillScan {
	return &SnapshotBackfillScan{
		snapshots: snapshots,
		reader:    reader,
		log:       log.With("worker", "invoicesvc_snapshot_backfill"),
		tick:      24 * time.Hour,
	}
}

func (w *SnapshotBackfillScan) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *SnapshotBackfillScan) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
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

func (w *SnapshotBackfillScan) RunOnce(ctx context.Context) {
	if w.reader == nil {
		w.log.Warn("snapshot backfill skipped — reader nil")
		return
	}
	ids, err := w.reader.IssuedInLast24h(ctx, 1000)
	if err != nil {
		w.log.Warn("scan invoices for backfill failed", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	created := 0
	skipped := 0
	for _, id := range ids {
		// Build a snapshot with no line items — the backfill creates a
		// minimal anchor row so the audit chain isn't gappy. Production
		// issuance hook fills line items on the primary path.
		if _, err := w.snapshots.CreateSnapshot(ctx, id, nil, nil); err != nil {
			// Duplicate on (invoice_id, snapshotted_at) is benign.
			skipped++
			continue
		}
		created++
	}
	w.log.Info("snapshot backfill complete", "candidates", len(ids), "created", created, "skipped", skipped)
}
