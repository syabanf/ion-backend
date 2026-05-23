package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// AssetStatus mirrors the CHECK on warehouse.assets.status. Serialized
// items live one of these states at any time.
type AssetStatus string

const (
	AssetStatusInStock         AssetStatus = "in_stock"
	AssetStatusDispatched      AssetStatus = "dispatched"
	AssetStatusInstalled       AssetStatus = "installed"
	AssetStatusReturned        AssetStatus = "returned"
	AssetStatusDecommissioned  AssetStatus = "decommissioned"
	AssetStatusCannibalized    AssetStatus = "cannibalized" // round-2: retrofit source
	AssetStatusDeployed        AssetStatus = "deployed"     // infrastructure on a network node
)

func (s AssetStatus) Valid() bool {
	switch s {
	case AssetStatusInStock, AssetStatusDispatched, AssetStatusInstalled,
		AssetStatusReturned, AssetStatusDecommissioned, AssetStatusCannibalized,
		AssetStatusDeployed:
		return true
	}
	return false
}

// Ownership describes who owns the physical device. Affects retrieval rules
// at termination (PRD §8 Device Return Flow).
type Ownership string

const (
	OwnershipION              Ownership = "ion_owned"
	OwnershipLeasedToCustomer Ownership = "leased_to_customer"
	OwnershipCustomerOwned    Ownership = "customer_owned"
)

func (o Ownership) Valid() bool {
	switch o {
	case OwnershipION, OwnershipLeasedToCustomer, OwnershipCustomerOwned:
		return true
	}
	return false
}

// Condition describes physical state at intake / return time.
type Condition string

const (
	ConditionNew         Condition = "new"
	ConditionRefurbished Condition = "refurbished"
	ConditionDamaged     Condition = "damaged"
)

func (c Condition) Valid() bool {
	switch c {
	case ConditionNew, ConditionRefurbished, ConditionDamaged:
		return true
	}
	return false
}

// Asset is one physical serialized unit. Maps 1:1 to a warehouse.assets row.
type Asset struct {
	ID                   uuid.UUID
	StockItemID          uuid.UUID
	WarehouseID          *uuid.UUID
	SerialNumber         string
	QRCode               string
	MACAddress           string
	FirmwareVersion      string
	Ownership            Ownership
	Condition            Condition
	Status               AssetStatus
	ReceivedAt           time.Time
	PurchaseCost         *float64
	PurchaseDate         *time.Time
	Distributor          string
	PurchaseOrderRef     string
	WarrantyExpiry       *time.Time
	IsRetrofit           bool
	CustomerID           *uuid.UUID
	AssignedTechnicianID *uuid.UUID
	WOID                 *uuid.UUID
	NetworkNodeID        *uuid.UUID
	Notes                string
	CreatedAt            time.Time
	UpdatedAt            time.Time

	// Wave 86 — first-class link to the PO that received this asset.
	// Supersedes the free-form PurchaseOrderRef TEXT column for new
	// rows; legacy rows keep the TEXT ref for back-compat.
	PurchaseOrderID *uuid.UUID
}

// NewAsset constructs an asset record. Used at intake.
//
// `warehouseID` is required — every serialized unit enters at a warehouse.
// `serialNumber` may be empty for retrofit assets that haven't been
// labeled yet (PRD §8A); the DB enforces uniqueness via the UNIQUE index.
func NewAsset(stockItemID, warehouseID uuid.UUID, serialNumber string, receivedAt time.Time) (*Asset, error) {
	if stockItemID == uuid.Nil {
		return nil, errors.Validation("asset.stock_item_required", "stock_item_id is required")
	}
	if warehouseID == uuid.Nil {
		return nil, errors.Validation("asset.warehouse_required", "warehouse_id is required at intake")
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	return &Asset{
		ID:           uuid.New(),
		StockItemID:  stockItemID,
		WarehouseID:  &warehouseID,
		SerialNumber: strings.TrimSpace(serialNumber),
		Ownership:    OwnershipION,
		Condition:    ConditionNew,
		Status:       AssetStatusInStock,
		ReceivedAt:   receivedAt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}
