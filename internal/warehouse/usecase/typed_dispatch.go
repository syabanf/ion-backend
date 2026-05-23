// Wave 117 — item-type-aware dispatch + consumption integration.
//
// The existing Wave 89b wo_dispatch flow ships explicit BOM line items.
// This module adds the typed-routing layer: when a dispatch line targets
// a Type 2 (cable) item, it CutSegment's from FIFO lots; Type 3 (consumable)
// hits ConsumeFromBatch FIFO; Type 4 (network infra) additionally upserts
// a netdev.devices row via the NetdevDeviceWriter bridge.
package usecase

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithNetdevWriter wires the cross-context bridge for Type 4 dispatch.
// Default is the SQL-only adapter writing directly to netdev.devices in
// the same DB. An HTTP adapter could replace it cleanly when netdev is
// hosted in its own binary.
func (s *Service) WithNetdevWriter(w port.NetdevDeviceWriter) *Service {
	s.netdevWriter = w
	return s
}

// TypedDispatchInput is the shape the caller passes for a single
// dispatch line. The router below dispatches to the right typed flow
// based on the underlying stock_item.category.
type TypedDispatchInput struct {
	WOID         uuid.UUID
	WarehouseID  uuid.UUID
	DispatchedBy uuid.UUID
	ItemID       uuid.UUID
	Qty          float64       // for cable (meters) / consumable (count)
	AssetID      *uuid.UUID    // serialized + infra path
	CustomerID   *uuid.UUID    // forwarded to LocationMovement
}

// TypedDispatchResult bundles whatever the typed flow produced. Callers
// don't have to peek inside; they get the audit anchors to render the
// dashboard.
type TypedDispatchResult struct {
	ItemType         domain.ItemType
	CableCutID       *uuid.UUID
	BatchConsumption *uuid.UUID
	LocationMovement *uuid.UUID
}

// DispatchTyped is the router. Returns a result that names which path
// fired + the audit-row id, so callers can drill into the right table.
func (s *Service) DispatchTyped(ctx context.Context, in TypedDispatchInput) (*TypedDispatchResult, error) {
	item, err := s.items.FindByID(ctx, in.ItemID)
	if err != nil {
		return nil, err
	}
	t := translateCategory(item.Category)
	res := &TypedDispatchResult{ItemType: t}
	switch t {
	case domain.ItemTypeCable:
		// FIFO-pick a lot or refuse. The catalog item must have at least
		// one in_stock lot at this warehouse.
		lot, err := s.pickOldestCableLot(ctx, in.ItemID, in.WarehouseID)
		if err != nil {
			return nil, err
		}
		cut, err := s.CutSegment(ctx, lot.ID, in.Qty, &in.WOID, in.DispatchedBy)
		if err != nil {
			return nil, err
		}
		res.CableCutID = &cut.ID
	case domain.ItemTypeConsumable:
		log, err := s.ConsumeFromBatch(ctx, nil, &in.ItemID, int(in.Qty), &in.WOID, in.DispatchedBy)
		if err != nil {
			return nil, err
		}
		res.BatchConsumption = &log.ID
	case domain.ItemTypeSerialized, domain.ItemTypeNetworkInfra:
		if in.AssetID == nil {
			return nil, derrors.Validation("typed_dispatch.asset_required",
				"serialized / infra dispatch requires asset_id")
		}
		// LocationMovement audit row + denormalized current_location update.
		if s.assetLocations != nil {
			mv, err := s.RecordMovement(ctx, port.RecordMovementInput{
				AssetID:         *in.AssetID,
				Kind:            domain.MovementKindDispatch,
				FromWarehouseID: &in.WarehouseID,
				WOID:            &in.WOID,
				CustomerID:      in.CustomerID,
				MovedBy:         in.DispatchedBy,
				Reason:          fmt.Sprintf("dispatch to wo %s", in.WOID),
			})
			if err != nil {
				return nil, err
			}
			res.LocationMovement = &mv.ID
		}
		// Type 4 — also write a netdev.devices row so the device lifecycle
		// service can take over from here.
		if t == domain.ItemTypeNetworkInfra && s.netdevWriter != nil {
			asset, err := s.assets.FindByID(ctx, *in.AssetID)
			if err != nil {
				return nil, err
			}
			kind := translateInfraKind(item)
			if err := s.netdevWriter.RegisterDevice(ctx, port.RegisterNetdevInput{
				SerialNo:     asset.SerialNumber,
				MACAddr:      asset.MACAddress,
				AssetTag:     "",
				Kind:         kind,
				Model:        item.Model,
				Manufacturer: item.Brand,
				WarehouseID:  &in.WarehouseID,
				CustomerID:   in.CustomerID,
			}); err != nil {
				// Type 4 bridge failures don't poison the dispatch path —
				// they're logged + surfaced in the audit. The
				// idempotent retry from the cron tick will heal.
				s.auditf(ctx, "netdev.bridge_failure",
					"asset=%s err=%v", asset.ID, err)
			}
		}
	default:
		return nil, derrors.Validation("typed_dispatch.unknown_type",
			"unknown item type for dispatch")
	}
	s.auditf(ctx, "typed_dispatch.fired",
		"wo=%s item=%s type=%s qty=%.2f", in.WOID, in.ItemID, t, in.Qty)
	return res, nil
}

// pickOldestCableLot — FIFO across in_stock lots at this warehouse for
// the given item. Refuses if no lot has enough remaining + no lot is in
// stock.
func (s *Service) pickOldestCableLot(ctx context.Context, itemID, warehouseID uuid.UUID) (*domain.CableLot, error) {
	if s.cableLots == nil {
		return nil, errCableNotConfigured()
	}
	wh := warehouseID
	lots, _, err := s.cableLots.List(ctx, port.CableLotListFilter{
		ItemID:      &itemID,
		WarehouseID: &wh,
		Status:      string(domain.CableLotStatusInStock),
		Limit:       1,
	})
	if err != nil {
		return nil, err
	}
	if len(lots) == 0 {
		// Try allocated lots that still have remainder.
		lots, _, err = s.cableLots.List(ctx, port.CableLotListFilter{
			ItemID:      &itemID,
			WarehouseID: &wh,
			Status:      string(domain.CableLotStatusAllocated),
			Limit:       1,
		})
		if err != nil {
			return nil, err
		}
	}
	if len(lots) == 0 {
		return nil, derrors.Conflict("cable.no_lot_available",
			"no in_stock or allocated cable lot for item at warehouse")
	}
	return &lots[0], nil
}

// translateInfraKind maps a catalog item's brand/model to the netdev
// device.kind enum. The mapping is conservative — anything we can't
// confidently classify falls through to 'other'.
func translateInfraKind(item *domain.StockItem) string {
	switch item.Category {
	case domain.CategoryInfrastructure:
		// Lean on the name for the typical Phase 1 set.
		switch {
		case containsAny(item.Name, "OLT"):
			return "olt_port"
		case containsAny(item.Name, "Switch"):
			return "switch"
		case containsAny(item.Name, "Router"):
			return "router"
		case containsAny(item.Name, "AP", "Access Point"):
			return "ap"
		}
	case domain.CategorySerializedDevice:
		switch {
		case containsAny(item.Name, "ONT"):
			return "ont"
		case containsAny(item.Name, "Router"):
			return "router"
		case containsAny(item.Name, "Switch"):
			return "switch"
		}
	}
	return "other"
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if len(s) == 0 || len(n) == 0 {
			continue
		}
		if matchSubstring(s, n) {
			return true
		}
	}
	return false
}

// matchSubstring — tiny strings.Contains shim to keep imports lean.
func matchSubstring(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			// case-insensitive ASCII compare
			if a >= 'a' && a <= 'z' {
				a -= 32
			}
			if b >= 'a' && b <= 'z' {
				b -= 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
