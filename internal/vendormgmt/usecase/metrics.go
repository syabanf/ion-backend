package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
)

// MetricsService implements port.MetricsUseCase.
type MetricsService struct {
	metrics   port.MetricsRepository
	providers port.ProviderRepository
}

func NewMetricsService(
	metrics port.MetricsRepository,
	providers port.ProviderRepository,
) *MetricsService {
	return &MetricsService{metrics: metrics, providers: providers}
}

var _ port.MetricsUseCase = (*MetricsService)(nil)

// RecordDailyMetric upserts one row into provider_metrics_daily. The
// unique key (provider_id, metric_date) collapses re-runs of the cron
// onto the same row — last-write-wins on the optional metric fields.
func (s *MetricsService) RecordDailyMetric(ctx context.Context, in port.RecordMetricInput) (*domain.DailyMetric, error) {
	m, err := domain.NewDailyMetric(in.ProviderID, in.MetricDate, in.JobsCompleted)
	if err != nil {
		return nil, err
	}
	m.OnTimeCompletionPct = in.OnTimeCompletionPct
	m.AvgResponseHours = in.AvgResponseHours
	m.TicketsResolved = in.TicketsResolved
	m.CustomerSatisfaction = in.CustomerSatisfaction
	if err := s.metrics.Upsert(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// AverageScoreForProvider rolls up the satisfaction column over the
// supplied lookback window. Defaults to 30 days when lookbackDays <= 0
// so the caller doesn't have to remember the magic number.
func (s *MetricsService) AverageScoreForProvider(ctx context.Context, providerID uuid.UUID, lookbackDays int) (float64, error) {
	if lookbackDays <= 0 {
		lookbackDays = 30
	}
	return s.metrics.AverageScore(ctx, providerID, lookbackDays)
}

// TopRatedProviders returns the ranked picker list. The repo does the
// JOIN + ORDER BY; this usecase just normalises filter defaults.
func (s *MetricsService) TopRatedProviders(ctx context.Context, f port.TopRatedFilter) ([]domain.Provider, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	return s.metrics.TopRated(ctx, f)
}

func (s *MetricsService) ListDailyMetrics(ctx context.Context, providerID uuid.UUID, from, to time.Time) ([]domain.DailyMetric, error) {
	return s.metrics.ListForProvider(ctx, providerID, from, to)
}
