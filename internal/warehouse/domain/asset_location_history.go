// Wave 117 — immutable per-asset location audit row.
package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// MovementKind names the operational verb for an asset location move.
// CHECK on the table enforces the same enum.
type MovementKind string

const (
	MovementKindReceive       MovementKind = "receive"
	MovementKindTransfer      MovementKind = "transfer"
	MovementKindDispatch      MovementKind = "dispatch"
	MovementKindReturn        MovementKind = "return"
	MovementKindConsume       MovementKind = "consume"
	MovementKindRetire        MovementKind = "retire"
	MovementKindInstall       MovementKind = "install"
	MovementKindDecommission  MovementKind = "decommission"
	MovementKindInTransit     MovementKind = "in_transit"
)

func (k MovementKind) Valid() bool {
	switch k {
	case MovementKindReceive, MovementKindTransfer, MovementKindDispatch,
		MovementKindReturn, MovementKindConsume, MovementKindRetire,
		MovementKindInstall, MovementKindDecommission, MovementKindInTransit:
		return true
	}
	return false
}

// LocationMovement is one row in warehouse.asset_location_history.
// Append-only.
type LocationMovement struct {
	ID                 uuid.UUID
	AssetID            uuid.UUID
	FromWarehouseID    *uuid.UUID
	ToWarehouseID      *uuid.UUID
	FromSubWarehouseID *uuid.UUID
	ToSubWarehouseID   *uuid.UUID
	MovementKind       MovementKind
	WOID               *uuid.UUID
	CustomerID         *uuid.UUID
	MovedBy            *uuid.UUID
	MovedAt            time.Time
	Reason             string
	LocationLabel      string
}

// NewLocationMovement validates inputs + stamps a timestamp.
func NewLocationMovement(assetID uuid.UUID, kind MovementKind) (*LocationMovement, error) {
	if assetID == uuid.Nil {
		return nil, errors.Validation("location_movement.asset_required", "asset_id is required")
	}
	if !kind.Valid() {
		return nil, errors.Validation("location_movement.kind_invalid", "movement_kind invalid")
	}
	return &LocationMovement{
		ID:           uuid.New(),
		AssetID:      assetID,
		MovementKind: kind,
		MovedAt:      time.Now().UTC(),
	}, nil
}
