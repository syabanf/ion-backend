// Wave 117 — Asset location tracking service.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func (s *Service) WithAssetLocations(r port.AssetLocationHistoryRepository) *Service {
	s.assetLocations = r
	return s
}

func errAssetLocationNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "asset_location.not_configured",
		"asset location repository is not configured for this service", nil)
}

// RecordMovement persists one row of asset_location_history AND syncs
// the denormalized assets.current_location_id + last_movement_at.
// Both writes happen in the same tx on the repo side.
func (s *Service) RecordMovement(ctx context.Context, in port.RecordMovementInput) (*domain.LocationMovement, error) {
	if s.assetLocations == nil {
		return nil, errAssetLocationNotConfigured()
	}
	mv, err := domain.NewLocationMovement(in.AssetID, in.Kind)
	if err != nil {
		return nil, err
	}
	mv.FromWarehouseID = in.FromWarehouseID
	mv.ToWarehouseID = in.ToWarehouseID
	mv.FromSubWarehouseID = in.FromSubWarehouseID
	mv.ToSubWarehouseID = in.ToSubWarehouseID
	mv.WOID = in.WOID
	mv.CustomerID = in.CustomerID
	mv.MovedBy = &in.MovedBy
	mv.Reason = in.Reason
	mv.LocationLabel = in.LocationLabel
	if err := s.assetLocations.Record(ctx, mv); err != nil {
		return nil, err
	}
	s.auditf(ctx, "asset.location_movement",
		"asset=%s kind=%s from_wh=%v to_wh=%v from_sub=%v to_sub=%v",
		in.AssetID, in.Kind, in.FromWarehouseID, in.ToWarehouseID, in.FromSubWarehouseID, in.ToSubWarehouseID)
	return mv, nil
}

// LocationHistory returns the per-asset audit trail, newest first.
func (s *Service) LocationHistory(ctx context.Context, assetID uuid.UUID, limit, offset int) ([]domain.LocationMovement, int, error) {
	if s.assetLocations == nil {
		return nil, 0, errAssetLocationNotConfigured()
	}
	return s.assetLocations.ListForAsset(ctx, assetID, limit, offset)
}

// CurrentLocation returns the denormalized current_location_id +
// last_movement_at from warehouse.assets.
func (s *Service) CurrentLocation(ctx context.Context, assetID uuid.UUID) (*uuid.UUID, *time.Time, error) {
	if s.assetLocations == nil {
		return nil, nil, errAssetLocationNotConfigured()
	}
	return s.assetLocations.CurrentLocation(ctx, assetID)
}

// ListInTransitAnomalies — TC-ALT-008: assets stuck in_transit longer
// than threshold get flagged for Warehouse Manager review.
func (s *Service) ListInTransitAnomalies(ctx context.Context, threshold time.Duration) ([]domain.LocationMovement, error) {
	if s.assetLocations == nil {
		return nil, errAssetLocationNotConfigured()
	}
	return s.assetLocations.ListInTransitOlderThan(ctx, threshold)
}
