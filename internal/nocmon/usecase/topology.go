package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
)

// TopologyService implements port.TopologyUseCase. The actual graph-
// building work lives behind port.TopologyBuilder (a cross-context
// port) so this service stays small: orchestrate the builder, persist
// the snapshot, and read the latest on demand.
type TopologyService struct {
	snapshots port.TopologySnapshotRepository
	builder   port.TopologyBuilder
}

func NewTopologyService(snapshots port.TopologySnapshotRepository, builder port.TopologyBuilder) *TopologyService {
	return &TopologyService{snapshots: snapshots, builder: builder}
}

var _ port.TopologyUseCase = (*TopologyService)(nil)

// RebuildSnapshot asks the bound TopologyBuilder for a fresh graph,
// validates + persists it, and returns the row. The builder runs in
// the caller's goroutine — large rebuilds should be invoked through
// /topology/{scope}/{id}/rebuild and the caller awaits the response.
func (s *TopologyService) RebuildSnapshot(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error) {
	snap, err := s.builder.BuildSnapshot(ctx, scope, scopeID)
	if err != nil {
		return nil, err
	}
	if err := s.snapshots.Create(ctx, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *TopologyService) GetLatest(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error) {
	return s.snapshots.FindLatest(ctx, scope, scopeID)
}
