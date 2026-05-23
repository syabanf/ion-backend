// Wave 117 — Sub-warehouse management.
package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func (s *Service) WithSubWarehouses(r port.SubWarehouseRepository) *Service {
	s.subWarehouses = r
	return s
}

func errSubWarehousesNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "sub_warehouse.not_configured",
		"sub-warehouse repository is not configured for this service", nil)
}

func (s *Service) CreateSubWarehouse(ctx context.Context, in port.CreateSubWarehouseInput) (*domain.SubWarehouse, error) {
	if s.subWarehouses == nil {
		return nil, errSubWarehousesNotConfigured()
	}
	// Verify parent exists — surfaces a clean 404 instead of an opaque FK error.
	if _, err := s.warehouses.FindByID(ctx, in.ParentWarehouseID); err != nil {
		return nil, err
	}
	sw, err := domain.NewSubWarehouse(in.ParentWarehouseID, in.OwnerUserID, in.Name, in.Code, in.OwnerRole)
	if err != nil {
		return nil, err
	}
	sw.IsMobile = in.IsMobile
	sw.VehicleID = in.VehicleID
	if err := s.subWarehouses.Create(ctx, sw); err != nil {
		return nil, err
	}
	s.auditf(ctx, "sub_warehouse.create", "id=%s parent=%s owner=%s", sw.ID, sw.ParentWarehouseID, sw.OwnerUserID)
	return sw, nil
}

func (s *Service) GetSubWarehouse(ctx context.Context, id uuid.UUID) (*domain.SubWarehouse, error) {
	if s.subWarehouses == nil {
		return nil, errSubWarehousesNotConfigured()
	}
	return s.subWarehouses.FindByID(ctx, id)
}

func (s *Service) ListSubWarehouses(ctx context.Context, f port.SubWarehouseListFilter) ([]domain.SubWarehouse, error) {
	if s.subWarehouses == nil {
		return nil, errSubWarehousesNotConfigured()
	}
	return s.subWarehouses.List(ctx, f)
}

// MySubWarehouses — claims-scoped list for the technician / TL mobile flow.
func (s *Service) MySubWarehouses(ctx context.Context, userID uuid.UUID) ([]domain.SubWarehouse, error) {
	if s.subWarehouses == nil {
		return nil, errSubWarehousesNotConfigured()
	}
	return s.subWarehouses.List(ctx, port.SubWarehouseListFilter{
		OwnerUserID: &userID,
		ActiveOnly:  true,
	})
}

// TransferToSub — typed transfer that records a LocationMovement audit
// row in the same tx as the asset.warehouse_id flip. Routing-aware:
// refuses Type 2 / Type 4 onto a mobile sub-warehouse (per domain.CanReceive).
func (s *Service) TransferToSub(ctx context.Context, assetID, subWarehouseID, byUserID uuid.UUID, reason string) error {
	if s.subWarehouses == nil {
		return errSubWarehousesNotConfigured()
	}
	if s.assetLocations == nil {
		return errAssetLocationNotConfigured()
	}
	sw, err := s.subWarehouses.FindByID(ctx, subWarehouseID)
	if err != nil {
		return err
	}
	asset, err := s.assets.FindByID(ctx, assetID)
	if err != nil {
		return err
	}
	// Resolve the underlying stock_item to read its item_type. Without a
	// direct ItemType getter on Asset, we fall back to the catalog row.
	item, err := s.items.FindByID(ctx, asset.StockItemID)
	if err != nil {
		return err
	}
	// Legacy stock_items.category is the live source until the new
	// item_type column propagates. Translate to ItemType for the check.
	t := translateCategory(item.Category)
	if !sw.CanReceive(t) {
		return derrors.Conflict("sub_warehouse.item_not_allowed",
			"this item type is not allowed at the target sub-warehouse")
	}
	mv, err := domain.NewLocationMovement(assetID, domain.MovementKindTransfer)
	if err != nil {
		return err
	}
	mv.FromWarehouseID = asset.WarehouseID
	mv.ToSubWarehouseID = &subWarehouseID
	mv.MovedBy = &byUserID
	mv.Reason = reason
	if err := s.assetLocations.Record(ctx, mv); err != nil {
		return err
	}
	s.auditf(ctx, "sub_warehouse.transfer_in", "asset=%s sub=%s by=%s", assetID, subWarehouseID, byUserID)
	return nil
}

// ReceiveFromSub — opposite leg, records the back-to-parent movement.
func (s *Service) ReceiveFromSub(ctx context.Context, assetID, subWarehouseID, parentWarehouseID, byUserID uuid.UUID, reason string) error {
	if s.assetLocations == nil {
		return errAssetLocationNotConfigured()
	}
	mv, err := domain.NewLocationMovement(assetID, domain.MovementKindReturn)
	if err != nil {
		return err
	}
	mv.FromSubWarehouseID = &subWarehouseID
	mv.ToWarehouseID = &parentWarehouseID
	mv.MovedBy = &byUserID
	mv.Reason = reason
	if err := s.assetLocations.Record(ctx, mv); err != nil {
		return err
	}
	s.auditf(ctx, "sub_warehouse.receive_from_sub", "asset=%s sub=%s parent=%s by=%s",
		assetID, subWarehouseID, parentWarehouseID, byUserID)
	return nil
}

// translateCategory maps the legacy stock_items.category enum to the
// new ItemType bucket. New rows set item_type directly; legacy rows
// fall through this translator.
func translateCategory(c domain.ItemCategory) domain.ItemType {
	switch c {
	case domain.CategoryCable:
		return domain.ItemTypeCable
	case domain.CategoryConsumable:
		return domain.ItemTypeConsumable
	case domain.CategoryInfrastructure:
		return domain.ItemTypeNetworkInfra
	}
	return domain.ItemTypeSerialized
}
