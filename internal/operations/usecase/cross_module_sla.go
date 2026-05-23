// Wave 126 — CrossModuleSLAService: aggregate per-module SLA stats into
// snapshot rows; serve the unified Ops dashboard view.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
)

// CrossModuleSLAService is the aggregator.
type CrossModuleSLAService struct {
	repo    port.CrossModuleSLASnapshotRepository
	readers []port.ModuleSLAReader
	log     *slog.Logger
}

// CrossModuleSLADeps groups dependencies.
type CrossModuleSLADeps struct {
	Repo    port.CrossModuleSLASnapshotRepository
	Readers []port.ModuleSLAReader
	Log     *slog.Logger
}

// NewCrossModuleSLAService builds the service.
func NewCrossModuleSLAService(deps CrossModuleSLADeps) *CrossModuleSLAService {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &CrossModuleSLAService{
		repo:    deps.Repo,
		readers: deps.Readers,
		log:     log.With("service", "operations.cross_module_sla"),
	}
}

// AggregateAll calls every registered reader and persists snapshot rows.
// Returns the count of modules successfully snapshotted.
func (s *CrossModuleSLAService) AggregateAll(ctx context.Context) (int, error) {
	if s == nil || s.repo == nil || len(s.readers) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	periodStart := now.Add(-24 * time.Hour)
	count := 0
	for _, reader := range s.readers {
		stats, err := reader.SLAStats(ctx, periodStart, now)
		if err != nil {
			s.log.Warn("module sla reader failed", "module", reader.ModuleName(), "err", err)
			continue
		}
		snap := &domain.SLASnapshot{
			ID:                  uuid.New(),
			Module:              reader.ModuleName(),
			AggregatedAt:        now,
			PeriodStart:         &periodStart,
			PeriodEnd:           &now,
			TotalAtRisk:         stats.TotalAtRisk,
			TotalBreached:       stats.TotalBreached,
			P50RemainingMinutes: stats.P50RemainingMinutes,
			P95RemainingMinutes: stats.P95RemainingMinutes,
			TopBreachers:        stats.TopBreachers,
		}
		if err := s.repo.Create(ctx, snap); err != nil {
			s.log.Warn("snapshot write failed", "module", reader.ModuleName(), "err", err)
			continue
		}
		count++
	}
	return count, nil
}

// LatestUnified returns the rolled-up view across modules.
func (s *CrossModuleSLAService) LatestUnified(ctx context.Context) (domain.UnifiedSLAView, error) {
	if s == nil || s.repo == nil {
		return domain.UnifiedSLAView{}, nil
	}
	snaps, err := s.repo.ListLatest(ctx)
	if err != nil {
		return domain.UnifiedSLAView{}, err
	}
	return domain.Rollup(snaps, 10), nil
}

// History returns the per-module snapshot history for the requested window.
func (s *CrossModuleSLAService) History(ctx context.Context, module domain.SLAModule, from, to time.Time, limit int) ([]domain.SLASnapshot, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	return s.repo.History(ctx, module, from, to, limit)
}
