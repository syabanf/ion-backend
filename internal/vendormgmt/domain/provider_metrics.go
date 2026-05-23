package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// DailyMetric is one row in vendor.provider_metrics_daily. The deriver
// cron writes one row per (provider, day); zero-job days are NOT
// written (we treat absence as "no activity" rather than carrying empty
// rows forward). The provider's denormalised RatingScore is recomputed
// from the rolling window of these rows.
type DailyMetric struct {
	ID                   uuid.UUID
	ProviderID           uuid.UUID
	MetricDate           time.Time // truncated to date in postgres
	JobsCompleted        int
	OnTimeCompletionPct  *float64
	AvgResponseHours     *float64
	TicketsResolved      int
	CustomerSatisfaction *float64
	CreatedAt            time.Time
}

// NewDailyMetric constructs a row with the required (provider, date,
// jobs_completed) tuple. The optional metrics get set by the caller
// after construction. JobsCompleted must be >= 0 — a negative count is
// a bug, not data.
func NewDailyMetric(providerID uuid.UUID, date time.Time, jobsCompleted int) (*DailyMetric, error) {
	if providerID == uuid.Nil {
		return nil, errors.Validation("metric.provider_required", "provider_id is required")
	}
	if jobsCompleted < 0 {
		return nil, errors.Validation("metric.jobs_negative", "jobs_completed must be >= 0")
	}
	// Truncate to date — the DB column is DATE, but we normalise here
	// so in-memory comparisons (e.g. dedupe in the cron) match the DB
	// representation.
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	return &DailyMetric{
		ID:            uuid.New(),
		ProviderID:    providerID,
		MetricDate:    d,
		JobsCompleted: jobsCompleted,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// ComputeOnTimePct returns the on-time completion percentage as a
// number in [0, 100]. Returns 0 when totalCompleted is 0 (no activity =
// no signal). completedOnTime is clamped to [0, totalCompleted] so a
// caller mistake doesn't produce a > 100% rate.
func ComputeOnTimePct(completedOnTime, totalCompleted int) float64 {
	if totalCompleted <= 0 {
		return 0
	}
	if completedOnTime < 0 {
		completedOnTime = 0
	}
	if completedOnTime > totalCompleted {
		completedOnTime = totalCompleted
	}
	return (float64(completedOnTime) / float64(totalCompleted)) * 100.0
}
