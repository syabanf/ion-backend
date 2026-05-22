// Package http — DTOs for the warehouse adapter.
//
// All HTTP-layer request/response shapes for warehouse live in this
// file (catalog, warehouses, stock items, inventory, assets, transfers,
// opname, movements, thresholds, alerts). Conversion helpers `toXxxDTO`
// sit next to their target type so a change to the wire shape touches
// one file instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or domain code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/warehouse/transfers
//     return?").
package http

import (
	"time"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
)

// =====================================================================
// Warehouses
// =====================================================================

type warehouseDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Code       string  `json:"code"`
	BranchID   *string `json:"branch_id,omitempty"`
	BranchName string  `json:"branch_name,omitempty"`
	BranchCode string  `json:"branch_code,omitempty"`
	Address    string  `json:"address"`
	Notes      string  `json:"notes"`
	Active     bool    `json:"active"`
	CreatedAt  string  `json:"created_at"`
}

func toWarehouseDTO(it port.WarehouseListItem) warehouseDTO {
	d := warehouseDTO{
		ID: it.Warehouse.ID.String(), Name: it.Warehouse.Name, Code: it.Warehouse.Code,
		BranchName: it.BranchName, BranchCode: it.BranchCode,
		Address: it.Warehouse.Address, Notes: it.Warehouse.Notes,
		Active:    it.Warehouse.Active,
		CreatedAt: it.Warehouse.CreatedAt.UTC().Format(time.RFC3339),
	}
	if it.Warehouse.BranchID != nil {
		s := it.Warehouse.BranchID.String()
		d.BranchID = &s
	}
	return d
}

type createWarehouseRequest struct {
	Name     string  `json:"name"`
	Code     string  `json:"code"`
	BranchID *string `json:"branch_id,omitempty"`
	Address  string  `json:"address"`
	Notes    string  `json:"notes"`
}

type updateWarehouseRequest struct {
	Name        *string `json:"name,omitempty"`
	BranchID    *string `json:"branch_id,omitempty"`
	ClearBranch bool    `json:"clear_branch,omitempty"`
	Address     *string `json:"address,omitempty"`
	Notes       *string `json:"notes,omitempty"`
	Active      *bool   `json:"active,omitempty"`
}

// =====================================================================
// Catalog (stock items)
// =====================================================================

type stockItemDTO struct {
	ID              string         `json:"id"`
	SKU             string         `json:"sku"`
	Name            string         `json:"name"`
	Category        string         `json:"category"`
	Brand           string         `json:"brand"`
	Model           string         `json:"model"`
	Spec            string         `json:"spec"`
	Unit            string         `json:"unit"`
	Serialized      bool           `json:"serialized"`
	DefaultUnitCost *float64       `json:"default_unit_cost,omitempty"`
	Active          bool           `json:"active"`
	Metadata        map[string]any `json:"metadata"`
}

func toStockItemDTO(it domain.StockItem) stockItemDTO {
	return stockItemDTO{
		ID: it.ID.String(), SKU: it.SKU, Name: it.Name,
		Category: string(it.Category), Brand: it.Brand, Model: it.Model, Spec: it.Spec,
		Unit: string(it.Unit), Serialized: it.Serialized,
		DefaultUnitCost: it.DefaultUnitCost, Active: it.Active, Metadata: it.Metadata,
	}
}

type createItemRequest struct {
	SKU             string   `json:"sku"`
	Name            string   `json:"name"`
	Category        string   `json:"category"`
	Brand           string   `json:"brand"`
	Model           string   `json:"model"`
	Spec            string   `json:"spec"`
	Unit            string   `json:"unit,omitempty"`
	DefaultUnitCost *float64 `json:"default_unit_cost,omitempty"`
}

type updateItemRequest struct {
	Name            *string  `json:"name,omitempty"`
	Brand           *string  `json:"brand,omitempty"`
	Model           *string  `json:"model,omitempty"`
	Spec            *string  `json:"spec,omitempty"`
	DefaultUnitCost *float64 `json:"default_unit_cost,omitempty"`
	Active          *bool    `json:"active,omitempty"`
}

// =====================================================================
// Inventory
// =====================================================================

type inventoryRowDTO struct {
	Item           stockItemDTO `json:"item"`
	Quantity       float64      `json:"quantity"`
	MinThreshold   *float64     `json:"min_threshold,omitempty"`
	BelowThreshold bool         `json:"below_threshold"`
	LastMovementAt *string      `json:"last_movement_at,omitempty"`
}

func toInventoryRowDTO(r port.InventoryRow) inventoryRowDTO {
	d := inventoryRowDTO{
		Item: toStockItemDTO(r.StockItem), Quantity: r.Quantity,
		MinThreshold: r.MinThreshold, BelowThreshold: r.BelowThreshold,
	}
	if r.LastMovementAt != nil {
		s := r.LastMovementAt.UTC().Format(time.RFC3339)
		d.LastMovementAt = &s
	}
	return d
}

// =====================================================================
// Intake (serialized + bulk)
// =====================================================================

type serialEntryDTO struct {
	SerialNumber string `json:"serial_number"`
	QRCode       string `json:"qr_code,omitempty"`
	MACAddress   string `json:"mac_address,omitempty"`
	Condition    string `json:"condition,omitempty"`
	Ownership    string `json:"ownership_type,omitempty"`
}

type intakeRequest struct {
	StockItemID      string           `json:"stock_item_id"`
	Quantity         float64          `json:"quantity,omitempty"`
	Serials          []serialEntryDTO `json:"serials,omitempty"`
	UnitCost         *float64         `json:"unit_cost,omitempty"`
	PurchaseDate     *string          `json:"purchase_date,omitempty"`
	Distributor      string           `json:"distributor,omitempty"`
	PurchaseOrderRef string           `json:"purchase_order_ref,omitempty"`
	WarrantyExpiry   *string          `json:"warranty_expiry,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	ReceivedAt       *string          `json:"received_at,omitempty"`
}

// =====================================================================
// Assets
// =====================================================================

type assetDTO struct {
	ID               string   `json:"id"`
	StockItemID      string   `json:"stock_item_id"`
	WarehouseID      *string  `json:"warehouse_id,omitempty"`
	SerialNumber     string   `json:"serial_number"`
	QRCode           string   `json:"qr_code"`
	MACAddress       string   `json:"mac_address"`
	Ownership        string   `json:"ownership_type"`
	Condition        string   `json:"condition"`
	Status           string   `json:"status"`
	ReceivedAt       string   `json:"received_at"`
	PurchaseCost     *float64 `json:"purchase_cost,omitempty"`
	Distributor      string   `json:"distributor"`
	PurchaseOrderRef string   `json:"purchase_order_ref"`
	IsRetrofit       bool     `json:"is_retrofit"`
}

func toAssetDTO(a domain.Asset) assetDTO {
	d := assetDTO{
		ID: a.ID.String(), StockItemID: a.StockItemID.String(),
		SerialNumber: a.SerialNumber, QRCode: a.QRCode, MACAddress: a.MACAddress,
		Ownership: string(a.Ownership), Condition: string(a.Condition),
		Status:           string(a.Status),
		ReceivedAt:       a.ReceivedAt.UTC().Format(time.RFC3339),
		PurchaseCost:     a.PurchaseCost,
		Distributor:      a.Distributor,
		PurchaseOrderRef: a.PurchaseOrderRef,
		IsRetrofit:       a.IsRetrofit,
	}
	if a.WarehouseID != nil {
		s := a.WarehouseID.String()
		d.WarehouseID = &s
	}
	return d
}

// =====================================================================
// Movements
// =====================================================================

type movementDTO struct {
	ID            string  `json:"id"`
	WarehouseID   string  `json:"warehouse_id"`
	StockItemID   string  `json:"stock_item_id"`
	AssetID       *string `json:"asset_id,omitempty"`
	MovementType  string  `json:"movement_type"`
	Quantity      float64 `json:"quantity"`
	Reason        string  `json:"reason,omitempty"`
	ReferenceType string  `json:"reference_type,omitempty"`
	ReferenceID   *string `json:"reference_id,omitempty"`
	PerformedBy   *string `json:"performed_by,omitempty"`
	PerformedAt   string  `json:"performed_at"`
}

func toMovementDTO(m domain.StockMovement) movementDTO {
	d := movementDTO{
		ID: m.ID.String(), WarehouseID: m.WarehouseID.String(), StockItemID: m.StockItemID.String(),
		MovementType: string(m.MovementType), Quantity: m.Quantity,
		Reason: m.Reason, ReferenceType: m.ReferenceType,
		PerformedAt: m.PerformedAt.UTC().Format(time.RFC3339),
	}
	if m.AssetID != nil {
		s := m.AssetID.String()
		d.AssetID = &s
	}
	if m.ReferenceID != nil {
		s := m.ReferenceID.String()
		d.ReferenceID = &s
	}
	if m.PerformedBy != nil {
		s := m.PerformedBy.String()
		d.PerformedBy = &s
	}
	return d
}

// =====================================================================
// Transfers
// =====================================================================

type transferItemDTO struct {
	ID          string  `json:"id"`
	StockItemID string  `json:"stock_item_id"`
	AssetID     *string `json:"asset_id,omitempty"`
	Quantity    float64 `json:"quantity"`
}

type transferDTO struct {
	ID                     string            `json:"id"`
	TransferNumber         string            `json:"transfer_number"`
	SourceWarehouseID      string            `json:"source_warehouse_id"`
	DestinationWarehouseID string            `json:"destination_warehouse_id"`
	Status                 string            `json:"status"`
	Notes                  string            `json:"notes"`
	DispatchedAt           *string           `json:"dispatched_at,omitempty"`
	ReceivedAt             *string           `json:"received_at,omitempty"`
	CreatedAt              string            `json:"created_at"`
	Items                  []transferItemDTO `json:"items"`
}

func toTransferDTO(t domain.Transfer) transferDTO {
	d := transferDTO{
		ID: t.ID.String(), TransferNumber: t.TransferNumber,
		SourceWarehouseID:      t.SourceWarehouseID.String(),
		DestinationWarehouseID: t.DestinationWarehouseID.String(),
		Status:                 string(t.Status),
		Notes:                  t.Notes,
		CreatedAt:              t.CreatedAt.UTC().Format(time.RFC3339),
		Items:                  []transferItemDTO{},
	}
	if t.DispatchedAt != nil {
		s := t.DispatchedAt.UTC().Format(time.RFC3339)
		d.DispatchedAt = &s
	}
	if t.ReceivedAt != nil {
		s := t.ReceivedAt.UTC().Format(time.RFC3339)
		d.ReceivedAt = &s
	}
	for _, it := range t.Items {
		i := transferItemDTO{
			ID: it.ID.String(), StockItemID: it.StockItemID.String(), Quantity: it.Quantity,
		}
		if it.AssetID != nil {
			s := it.AssetID.String()
			i.AssetID = &s
		}
		d.Items = append(d.Items, i)
	}
	return d
}

type createTransferRequest struct {
	SourceWarehouseID      string `json:"source_warehouse_id"`
	DestinationWarehouseID string `json:"destination_warehouse_id"`
	Notes                  string `json:"notes,omitempty"`
	Items                  []struct {
		StockItemID string  `json:"stock_item_id"`
		AssetID     *string `json:"asset_id,omitempty"`
		Quantity    float64 `json:"quantity"`
	} `json:"items"`
}

// =====================================================================
// Thresholds
// =====================================================================

type setThresholdRequest struct {
	// Send a number (>= 0) to set; send null/omitted to clear.
	MinThreshold *float64 `json:"min_threshold"`
}

// =====================================================================
// Alerts
// =====================================================================

type alertDTO struct {
	WarehouseID    string   `json:"warehouse_id"`
	WarehouseCode  string   `json:"warehouse_code"`
	WarehouseName  string   `json:"warehouse_name"`
	BranchID       *string  `json:"branch_id,omitempty"`
	BranchCode     string   `json:"branch_code,omitempty"`
	BranchName     string   `json:"branch_name,omitempty"`
	BranchLevel    string   `json:"branch_level,omitempty"`
	StockItemID    string   `json:"stock_item_id"`
	StockItemSKU   string   `json:"stock_item_sku"`
	StockItemName  string   `json:"stock_item_name"`
	Unit           string   `json:"unit"`
	Quantity       float64  `json:"quantity"`
	MinThreshold   float64  `json:"min_threshold"`
	Shortfall      float64  `json:"shortfall"`
	EscalationPath []string `json:"escalation_path,omitempty"`
}

func toAlertDTO(a domain.StockAlert) alertDTO {
	d := alertDTO{
		WarehouseID:   a.WarehouseID.String(),
		WarehouseCode: a.WarehouseCode,
		WarehouseName: a.WarehouseName,
		BranchCode:    a.BranchCode,
		BranchName:    a.BranchName,
		BranchLevel:   a.BranchLevel,
		StockItemID:   a.StockItemID.String(),
		StockItemSKU:  a.StockItemSKU,
		StockItemName: a.StockItemName,
		Unit:          a.Unit,
		Quantity:      a.Quantity,
		MinThreshold:  a.MinThreshold,
		Shortfall:     a.Shortfall,
	}
	if a.BranchID != nil {
		s := a.BranchID.String()
		d.BranchID = &s
	}
	d.EscalationPath = make([]string, 0, len(a.EscalationPath))
	for _, id := range a.EscalationPath {
		d.EscalationPath = append(d.EscalationPath, id.String())
	}
	return d
}

// =====================================================================
// Opname
// =====================================================================

type opnameCountDTO struct {
	ID                   string  `json:"id"`
	StockItemID          string  `json:"stock_item_id"`
	StockItemSKU         string  `json:"stock_item_sku"`
	StockItemName        string  `json:"stock_item_name"`
	Unit                 string  `json:"unit"`
	IsCable              bool    `json:"is_cable"`
	ExpectedQty          float64 `json:"expected_qty"`
	CountedQty           float64 `json:"counted_qty"`
	Variance             float64 `json:"variance"`
	CableRemnantDecision *string `json:"cable_remnant_decision,omitempty"`
	Notes                string  `json:"notes,omitempty"`
	CountedAt            string  `json:"counted_at"`
}

type opnameSessionDTO struct {
	ID            string           `json:"id"`
	SessionNumber string           `json:"session_number"`
	WarehouseID   string           `json:"warehouse_id"`
	WarehouseCode string           `json:"warehouse_code"`
	WarehouseName string           `json:"warehouse_name"`
	Status        string           `json:"status"`
	StartedBy     *string          `json:"started_by,omitempty"`
	StartedAt     string           `json:"started_at"`
	CommittedAt   *string          `json:"committed_at,omitempty"`
	CancelledAt   *string          `json:"cancelled_at,omitempty"`
	Notes         string           `json:"notes,omitempty"`
	Counts        []opnameCountDTO `json:"counts,omitempty"`
}

func toOpnameDTO(v port.OpnameView) opnameSessionDTO {
	s := v.Session
	d := opnameSessionDTO{
		ID:            s.ID.String(),
		SessionNumber: s.SessionNumber,
		WarehouseID:   s.WarehouseID.String(),
		WarehouseCode: v.WarehouseCode,
		WarehouseName: v.WarehouseName,
		Status:        string(s.Status),
		StartedAt:     s.StartedAt.UTC().Format(time.RFC3339),
		Notes:         s.Notes,
	}
	if s.StartedBy != nil {
		x := s.StartedBy.String()
		d.StartedBy = &x
	}
	if s.CommittedAt != nil {
		x := s.CommittedAt.UTC().Format(time.RFC3339)
		d.CommittedAt = &x
	}
	if s.CancelledAt != nil {
		x := s.CancelledAt.UTC().Format(time.RFC3339)
		d.CancelledAt = &x
	}
	for _, cv := range v.Counts {
		cd := opnameCountDTO{
			ID:            cv.Count.ID.String(),
			StockItemID:   cv.Count.StockItemID.String(),
			StockItemSKU:  cv.ItemSKU,
			StockItemName: cv.ItemName,
			Unit:          cv.ItemUnit,
			IsCable:       cv.IsCable,
			ExpectedQty:   cv.Count.ExpectedQty,
			CountedQty:    cv.Count.CountedQty,
			Variance:      cv.Count.Variance,
			Notes:         cv.Count.Notes,
			CountedAt:     cv.Count.CountedAt.UTC().Format(time.RFC3339),
		}
		if cv.Count.CableRemnantDecision != nil {
			x := string(*cv.Count.CableRemnantDecision)
			cd.CableRemnantDecision = &x
		}
		d.Counts = append(d.Counts, cd)
	}
	return d
}

type startOpnameRequest struct {
	WarehouseID string `json:"warehouse_id"`
	Notes       string `json:"notes,omitempty"`
}

type upsertCountRequest struct {
	StockItemID          string  `json:"stock_item_id"`
	CountedQty           float64 `json:"counted_qty"`
	CableRemnantDecision *string `json:"cable_remnant_decision,omitempty"`
	Notes                string  `json:"notes,omitempty"`
}
