// Package cron ships the vendor-svc periodic workers.
//
// Wave 107 ships one worker:
//
//   - MetricsDeriverDaily — runs daily, scans EWO completions for the
//     prior day, derives per-provider metrics, upserts to
//     vendor.provider_metrics_daily. Idempotent: the unique key
//     (provider_id, metric_date) collapses repeat ticks onto the same
//     row.
//
// The deriver reaches across to enterprise.ewo_completion_log via raw
// SQL — kept here because the enterprise context's audit row is the
// authoritative source, and the vendor module is otherwise
// self-contained. When vendor-svc moves to its own binary against a
// dedicated DB, the deriver gets a real port (cross-service read).
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
)

// MetricsDeriverDaily is the daily aggregation worker.
type MetricsDeriverDaily struct {
	pool    *pgxpool.Pool
	metrics port.MetricsUseCase
	log     *slog.Logger
}

func NewMetricsDeriverDaily(
	pool *pgxpool.Pool,
	metrics port.MetricsUseCase,
	log *slog.Logger,
) *MetricsDeriverDaily {
	return &MetricsDeriverDaily{
		pool:    pool,
		metrics: metrics,
		log:     log.With("worker", "vendor_metrics_deriver_daily"),
	}
}

// Start spawns the worker goroutine. Cancel ctx to stop.
func (d *MetricsDeriverDaily) Start(ctx context.Context) {
	go d.run(ctx)
}

func (d *MetricsDeriverDaily) run(ctx context.Context) {
	// First tick fires ~1 minute after boot so the service is fully up
	// without making the test cron-roundtrip wait 24h. After that,
	// every 24h.
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Minute):
	}
	d.RunOnce(ctx, time.Now().AddDate(0, 0, -1))
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.RunOnce(ctx, time.Now().AddDate(0, 0, -1))
		}
	}
}

// RunOnce derives metrics for the given target date. Exposed for tests
// + a future manual-replay admin route. day is normalised to UTC date
// boundaries before the query.
func (d *MetricsDeriverDaily) RunOnce(ctx context.Context, day time.Time) {
	target := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	next := target.AddDate(0, 0, 1)

	// Aggregate EWO completions for the target day, bucketed by the
	// assigned_provider_company_id column on the BOQ line. The query
	// joins ewo_completion_log → ewos → boq_lines so we get the
	// provider id without dragging the entire BOQ across.
	//
	// The enterprise tables may not exist in some deployments
	// (vendor-svc could move to its own DB). We catch the query error +
	// log it rather than crashing the worker — the cron then tries again
	// tomorrow.
	rows, err := d.pool.Query(ctx, `
		SELECT bl.assigned_provider_company_id                              AS provider_id,
		       COUNT(*)                                                    AS jobs,
		       COUNT(*) FILTER (
		           WHERE l.planned_finish IS NULL
		              OR l.actual_finish <= l.planned_finish
		       )                                                           AS on_time,
		       AVG(l.response_hours)                                       AS avg_resp
		FROM enterprise.ewo_completion_log l
		JOIN enterprise.ewos e ON e.id = l.ewo_id
		JOIN enterprise.boq_lines bl ON bl.boq_version_id = e.boq_version_id
		WHERE l.actual_finish >= $1
		  AND l.actual_finish <  $2
		  AND bl.assigned_provider_company_id IS NOT NULL
		GROUP BY bl.assigned_provider_company_id
	`, target, next)
	if err != nil {
		d.log.Info("deriver: source query failed (enterprise tables may be unavailable)",
			"err", err, "target_date", target.Format("2006-01-02"))
		return
	}
	defer rows.Close()

	type agg struct {
		providerID uuid.UUID
		jobs       int
		onTime     int
		avgResp    *float64
	}
	var batch []agg
	for rows.Next() {
		var a agg
		if err := rows.Scan(&a.providerID, &a.jobs, &a.onTime, &a.avgResp); err != nil {
			d.log.Warn("deriver: scan failed", "err", err)
			continue
		}
		batch = append(batch, a)
	}
	if len(batch) == 0 {
		d.log.Info("deriver: no completions for target day",
			"target_date", target.Format("2006-01-02"))
		return
	}

	d.log.Info("deriver: upserting metrics",
		"target_date", target.Format("2006-01-02"),
		"providers", len(batch))
	for _, a := range batch {
		onTimePct := domain.ComputeOnTimePct(a.onTime, a.jobs)
		in := port.RecordMetricInput{
			ProviderID:          a.providerID,
			MetricDate:          target,
			JobsCompleted:       a.jobs,
			OnTimeCompletionPct: &onTimePct,
			AvgResponseHours:    a.avgResp,
		}
		if _, err := d.metrics.RecordDailyMetric(ctx, in); err != nil {
			d.log.Warn("deriver: upsert failed",
				"provider_id", a.providerID,
				"err", err)
		}
	}
}
