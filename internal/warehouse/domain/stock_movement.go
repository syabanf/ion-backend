package domain

import (
	"time"

	"github.com/google/uuid"
)

// MovementType mirrors the CHECK on warehouse.stock_movements.movement_type.
type MovementType string

const (
	MovementIntake           MovementType = "intake"
	MovementDispatch         MovementType = "dispatch"
	MovementReturn           MovementType = "return"
	MovementTransferOut      MovementType = "transfer_out"
	MovementTransferIn       MovementType = "transfer_in"
	MovementOpnameAdjustment MovementType = "opname_adjustment"
	MovementRetrofitConsume  MovementType = "retrofit_consume"
	MovementRetrofitProduce  MovementType = "retrofit_produce"
	MovementDispose          MovementType = "dispose"
)

// StockMovement is one append-only audit row.
type StockMovement struct {
	ID            uuid.UUID
	WarehouseID   uuid.UUID
	StockItemID   uuid.UUID
	AssetID       *uuid.UUID
	MovementType  MovementType
	Quantity      float64
	Reason        string
	ReferenceType string
	ReferenceID   *uuid.UUID
	PerformedBy   *uuid.UUID
	PerformedAt   time.Time
}

// StockLevel is the (warehouse, stock_item) aggregate for non-serialized
// items. Serialized item counts come from COUNT(*) on assets.
type StockLevel struct {
	ID           uuid.UUID
	WarehouseID  uuid.UUID
	StockItemID  uuid.UUID
	Quantity     float64
	MinThreshold *float64
	UpdatedAt    time.Time
}
