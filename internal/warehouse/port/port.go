// Package port defines the contracts between the warehouse usecase layer
// and the world outside it. Same hexagonal pattern as identity / network.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
)

// =====================================================================
// Driving ports (UseCase contract)
// =====================================================================

// --- Warehouses ---

type CreateWarehouseInput struct {
	Name     string
	Code     string
	BranchID *uuid.UUID
	Address  string
	Notes    string
}

type UpdateWarehouseInput struct {
	ID          uuid.UUID
	Name        *string
	BranchID    *uuid.UUID
	ClearBranch bool
	Address     *string
	Notes       *string
	Active      *bool
}

type WarehouseListItem struct {
	Warehouse  domain.Warehouse
	BranchName string
	BranchCode string
}

// --- Stock items ---

type CreateStockItemInput struct {
	SKU             string
	Name            string
	Category        domain.ItemCategory
	Brand           string
	Model           string
	Spec            string
	Unit            domain.Unit // if zero, derived from category in NewStockItem
	DefaultUnitCost *float64
}

type UpdateStockItemInput struct {
	ID              uuid.UUID
	Name            *string
	Brand           *string
	Model           *string
	Spec            *string
	DefaultUnitCost *float64
	Active          *bool
}

type StockItemListFilter struct {
	Search   string
	Category string // empty = all
	Active   *bool
	Limit    int
	Offset   int
}

// --- Stock intake ---

// IntakeInput receives stock into a warehouse. For serialized items each
// entry in `Serials` produces one asset row; quantity is implicit
// (len(Serials)). For non-serialized, `Quantity` is meters/count.
type IntakeInput struct {
	WarehouseID      uuid.UUID
	StockItemID      uuid.UUID
	Quantity         float64       // non-serialized
	Serials          []SerialEntry // serialized
	UnitCost         *float64      // optional override of stock_item.default_unit_cost
	PurchaseDate     *time.Time
	Distributor      string
	PurchaseOrderRef string
	WarrantyExpiry   *time.Time
	Reason           string
	ReceivedAt       time.Time
}

// SerialEntry is one serialized unit being received.
type SerialEntry struct {
	SerialNumber string
	QRCode       string
	MACAddress   string
	Condition    domain.Condition
	Ownership    domain.Ownership
}

// IntakeResult — what callers get back. For non-serialized this carries the
// updated stock_level; for serialized it lists the created asset IDs.
type IntakeResult struct {
	StockLevel   *domain.StockLevel
	CreatedAssets []uuid.UUID
}

// --- Inventory views ---

type InventoryRow struct {
	StockItem domain.StockItem
	// For serialized items: count of assets in_stock at this warehouse.
	// For non-serialized: stock_levels.quantity.
	Quantity     float64
	MinThreshold *float64
	BelowThreshold bool
	// Latest movement timestamp (for "last activity" UX).
	LastMovementAt *time.Time
}

type InventoryFilter struct {
	WarehouseID uuid.UUID
	Category    string
	Search      string
	BelowOnly   bool // only return rows below min_threshold
	// OrderBy controls how serialized items are sorted within the row.
	// The inventory view itself sorts by item name; this knob only
	// affects the LastMovementAt tie-breaker for assets backing each
	// item. "fifo" surfaces the oldest received_at first; default LIFO.
	OrderBy string
	Limit   int
	Offset  int
}

// --- Assets ---

// ValuationReader resolves platform_config.inventory_valuation_method
// ("fifo" / "lifo") at call time, with caching managed by the
// implementation. The warehouse usecase consults this when a list
// filter doesn't specify an explicit `order_by`, so the same code
// path serves both an admin override and the platform-wide default.
type ValuationReader interface {
	InventoryValuationMethod(ctx context.Context) string
}

type AssetListFilter struct {
	WarehouseID *uuid.UUID
	StockItemID *uuid.UUID
	Status      string
	Search      string // matches serial or qr
	// OrderBy controls the dispatch sort order:
	//   "" or "lifo" — default; newest received_at first (last-in-first-out)
	//   "fifo"        — oldest received_at first (first-in-first-out)
	// The warehouse PRD (round-3) calls for FIFO/LIFO selection driven
	// by platform_config.inventory_valuation_method. Round-3 exposes
	// the knob on this filter; round-4 collapses it into a default read
	// from platform_config so the dispatch flow doesn't have to choose
	// each time.
	OrderBy string
	Limit   int
	Offset  int
}

// --- Transfers ---

type CreateTransferInput struct {
	SourceWarehouseID      uuid.UUID
	DestinationWarehouseID uuid.UUID
	Notes                  string
	CreatedBy              uuid.UUID
	Items                  []TransferItemInput
}

type TransferItemInput struct {
	StockItemID uuid.UUID
	AssetID     *uuid.UUID // for serialized — must be in_stock at source
	Quantity    float64    // for non-serialized
}

// =====================================================================
// Suppliers (CRM-Sales-Enterprise PRD §5.1 vendor registry)
// =====================================================================

type CreateSupplierInput struct {
	Code          string
	CompanyName   string
	ContactPerson string
	Phone         string
	Email         string
	Address       string
	PaymentTerms  string
	NPWP          string
	NIB           string
	CategoryTags  []string
	Notes         string
}

type UpdateSupplierInput struct {
	ID            uuid.UUID
	CompanyName   *string
	ContactPerson *string
	Phone         *string
	Email         *string
	Address       *string
	PaymentTerms  *string
	NPWP          *string
	NIB           *string
	CategoryTags  *[]string
	Notes         *string
	Active        *bool
}

type SupplierListFilter struct {
	Search          string
	ActiveOnly      bool
	IncludeInactive bool
	Limit           int
	Offset          int
}

// UseCase is the contract the HTTP handler depends on.
type UseCase interface {
	// Suppliers
	ListSuppliers(ctx context.Context, f SupplierListFilter) ([]domain.Supplier, int, error)
	GetSupplier(ctx context.Context, id uuid.UUID) (*domain.Supplier, error)
	CreateSupplier(ctx context.Context, in CreateSupplierInput) (*domain.Supplier, error)
	UpdateSupplier(ctx context.Context, in UpdateSupplierInput) (*domain.Supplier, error)

	// Warehouses
	ListWarehouses(ctx context.Context, activeOnly bool) ([]WarehouseListItem, error)
	GetWarehouse(ctx context.Context, id uuid.UUID) (*WarehouseListItem, error)
	CreateWarehouse(ctx context.Context, in CreateWarehouseInput) (*domain.Warehouse, error)
	UpdateWarehouse(ctx context.Context, in UpdateWarehouseInput) (*domain.Warehouse, error)

	// Stock catalog
	ListStockItems(ctx context.Context, f StockItemListFilter) ([]domain.StockItem, int, error)
	GetStockItem(ctx context.Context, id uuid.UUID) (*domain.StockItem, error)
	CreateStockItem(ctx context.Context, in CreateStockItemInput) (*domain.StockItem, error)
	UpdateStockItem(ctx context.Context, in UpdateStockItemInput) (*domain.StockItem, error)

	// Intake
	Intake(ctx context.Context, in IntakeInput, performedBy uuid.UUID) (*IntakeResult, error)

	// Inventory
	Inventory(ctx context.Context, f InventoryFilter) ([]InventoryRow, int, error)

	// Assets
	ListAssets(ctx context.Context, f AssetListFilter) ([]domain.Asset, int, error)
	GetAsset(ctx context.Context, id uuid.UUID) (*domain.Asset, error)

	// Movements (audit)
	ListMovements(ctx context.Context, warehouseID uuid.UUID, limit, offset int) ([]domain.StockMovement, int, error)

	// Transfers
	CreateTransfer(ctx context.Context, in CreateTransferInput) (*domain.Transfer, error)
	ListTransfers(ctx context.Context, status string, limit, offset int) ([]domain.Transfer, int, error)
	GetTransfer(ctx context.Context, id uuid.UUID) (*domain.Transfer, error)
	DispatchTransfer(ctx context.Context, id, performedBy uuid.UUID) (*domain.Transfer, error)
	ReceiveTransfer(ctx context.Context, id, performedBy uuid.UUID) (*domain.Transfer, error)
	CancelTransfer(ctx context.Context, id uuid.UUID) error

	// Thresholds (M3 r2)
	SetThreshold(ctx context.Context, in SetThresholdInput) error

	// Alerts (M3 r2)
	ListStockAlerts(ctx context.Context, f AlertFilter) ([]domain.StockAlert, error)

	// Opname (M3 r2)
	StartOpname(ctx context.Context, in StartOpnameInput) (*OpnameView, error)
	GetOpname(ctx context.Context, id uuid.UUID) (*OpnameView, error)
	ListOpnameSessions(ctx context.Context, warehouseID *uuid.UUID, status string, limit, offset int) ([]OpnameView, int, error)
	UpsertOpnameCount(ctx context.Context, in UpsertOpnameCountInput) (*domain.OpnameCount, error)
	CommitOpname(ctx context.Context, id, performedBy uuid.UUID) (*OpnameView, error)
	CancelOpname(ctx context.Context, id, performedBy uuid.UUID) (*OpnameView, error)
}

// =====================================================================
// M3 r2 Inputs / Views
// =====================================================================

type SetThresholdInput struct {
	WarehouseID  uuid.UUID
	StockItemID  uuid.UUID
	MinThreshold *float64 // nil = clear threshold
}

type AlertFilter struct {
	// BranchID, when non-nil, restricts results to warehouses whose
	// branch matches OR is a descendant of this branch. This is the PRD
	// "parent branches see sub-branch alerts" rule: a Regional user
	// supplies their regional branch_id; the query returns alerts from
	// all Areas and Sub Areas under it.
	BranchID *uuid.UUID
	// IncludeZero, when false (default), excludes items where
	// quantity == 0 AND min_threshold IS NULL. We use min_threshold as
	// the on/off switch — no threshold set means "no alert".
	IncludeZero bool
}

type StartOpnameInput struct {
	WarehouseID uuid.UUID
	StartedBy   uuid.UUID
	Notes       string
}

// OpnameView — session + denormalized warehouse label + the counts.
type OpnameView struct {
	Session       domain.OpnameSession
	WarehouseCode string
	WarehouseName string
	Counts        []OpnameCountView
}

type OpnameCountView struct {
	Count     domain.OpnameCount
	ItemSKU   string
	ItemName  string
	ItemUnit  string
	IsCable   bool // category == 'cable'
}

type UpsertOpnameCountInput struct {
	SessionID            uuid.UUID
	StockItemID          uuid.UUID
	CountedQty           float64
	CableRemnantDecision *domain.CableRemnantDecision
	Notes                string
	CountedBy            uuid.UUID
}

// =====================================================================
// M3 r2 Repositories
// =====================================================================

type ThresholdRepository interface {
	// Upsert the (warehouse, item) row with min_threshold. When the row
	// doesn't exist yet we insert with quantity=0; this keeps the alert
	// computable even before any stock has flowed through.
	Set(ctx context.Context, warehouseID, itemID uuid.UUID, threshold *float64) error
}

type AlertRepository interface {
	// ListBelowThreshold computes alerts at read time by joining
	// stock_levels with warehouses/branches/stock_items. Returns rows
	// where min_threshold IS NOT NULL AND quantity < min_threshold.
	// branchID, when non-nil, filters to that branch and all descendants.
	ListBelowThreshold(ctx context.Context, branchID *uuid.UUID) ([]domain.StockAlert, error)
}

type OpnameRepository interface {
	CreateSession(ctx context.Context, s *domain.OpnameSession) error
	FindSession(ctx context.Context, id uuid.UUID) (*OpnameView, error)
	ListSessions(ctx context.Context, warehouseID *uuid.UUID, status string, limit, offset int) ([]OpnameView, int, error)
	UpdateSessionStatus(ctx context.Context, id uuid.UUID, status domain.OpnameStatus, ts time.Time) error
	UpsertCount(ctx context.Context, c *domain.OpnameCount) (*domain.OpnameCount, error)
	ListCounts(ctx context.Context, sessionID uuid.UUID) ([]OpnameCountView, error)
}

// =====================================================================
// Driven ports (repositories)
// =====================================================================

type WarehouseRepository interface {
	List(ctx context.Context, activeOnly bool) ([]WarehouseListItem, error)
	FindByID(ctx context.Context, id uuid.UUID) (*WarehouseListItem, error)
	Create(ctx context.Context, w *domain.Warehouse) error
	Update(ctx context.Context, in UpdateWarehouseInput) (*domain.Warehouse, error)
}

type SupplierRepository interface {
	List(ctx context.Context, f SupplierListFilter) ([]domain.Supplier, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Supplier, error)
	FindByCode(ctx context.Context, code string) (*domain.Supplier, error)
	Create(ctx context.Context, s *domain.Supplier) error
	Update(ctx context.Context, in UpdateSupplierInput) (*domain.Supplier, error)
}

type StockItemRepository interface {
	List(ctx context.Context, f StockItemListFilter) ([]domain.StockItem, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.StockItem, error)
	FindBySKU(ctx context.Context, sku string) (*domain.StockItem, error)
	Create(ctx context.Context, item *domain.StockItem) error
	Update(ctx context.Context, in UpdateStockItemInput) (*domain.StockItem, error)
}

type AssetRepository interface {
	List(ctx context.Context, f AssetListFilter) ([]domain.Asset, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Asset, error)
	Create(ctx context.Context, a *domain.Asset) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.AssetStatus, warehouseID *uuid.UUID) error
}

type StockLevelRepository interface {
	Get(ctx context.Context, warehouseID, itemID uuid.UUID) (*domain.StockLevel, error)
	UpsertDelta(ctx context.Context, warehouseID, itemID uuid.UUID, delta float64) (*domain.StockLevel, error)
}

type MovementRepository interface {
	Record(ctx context.Context, m *domain.StockMovement) error
	List(ctx context.Context, warehouseID uuid.UUID, limit, offset int) ([]domain.StockMovement, int, error)
}

type InventoryRepository interface {
	Inventory(ctx context.Context, f InventoryFilter) ([]InventoryRow, int, error)
}

type TransferRepository interface {
	Create(ctx context.Context, t *domain.Transfer) error
	List(ctx context.Context, status string, limit, offset int) ([]domain.Transfer, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Transfer, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.TransferStatus, ts *time.Time) error
}
