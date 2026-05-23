// Wave 117 — port additions for warehouse depth.
//
// Each new repository is a focused contract; the usecase service composes
// them via opt-in builders (same pattern as WithSuppliers / WithR2 / WithWODispatch).
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
)

// =====================================================================
// Item categories (Wave 117)
// =====================================================================

// CreateItemCategoryInput drives the usecase + handler.
type CreateItemCategoryInput struct {
	Code                       string
	Name                       string
	TypeCode                   domain.ItemType
	ParentID                   *uuid.UUID
	Description                string
	DefaultUnit                string
	SubWarehouseAllowedDefault *bool
	RequiresSerialAtIntake     *bool
}

type UpdateItemCategoryInput struct {
	ID                         uuid.UUID
	Name                       *string
	Description                *string
	ParentID                   *uuid.UUID
	ClearParent                bool
	DefaultUnit                *string
	SubWarehouseAllowedDefault *bool
	RequiresSerialAtIntake     *bool
	Active                     *bool
}

type ItemCategoryListFilter struct {
	TypeCode   string
	ActiveOnly bool
	ParentID   *uuid.UUID
}

type ItemCategoryRepository interface {
	Create(ctx context.Context, c *domain.ItemCategoryDef) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ItemCategoryDef, error)
	FindByCode(ctx context.Context, code string) (*domain.ItemCategoryDef, error)
	List(ctx context.Context, f ItemCategoryListFilter) ([]domain.ItemCategoryDef, error)
	Update(ctx context.Context, in UpdateItemCategoryInput) (*domain.ItemCategoryDef, error)
}

// =====================================================================
// Cable lots + cuts (Wave 117)
// =====================================================================

type ReceiveCableLotInput struct {
	ItemID              uuid.UUID
	LotNumber           string
	TotalLengthMeters   float64
	DrumSerial          string
	SupplierID          *uuid.UUID
	WarehouseID         uuid.UUID
	UnitCostPerMeter    *float64
	Notes               string
}

type CableLotListFilter struct {
	ItemID      *uuid.UUID
	WarehouseID *uuid.UUID
	Status      string
	LowRemainingThresholdMeters *float64 // when set, only lots with remaining < threshold
	Limit       int
	Offset      int
}

type CableLotRepository interface {
	Create(ctx context.Context, l *domain.CableLot) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.CableLot, error)
	List(ctx context.Context, f CableLotListFilter) ([]domain.CableLot, int, error)
	// PersistCut atomically updates the lot's remaining+status AND inserts
	// the CableCut audit row in a single tx. Returns the persisted cut row.
	PersistCut(ctx context.Context, lot *domain.CableLot, cut *domain.CableCut) error
	// Dispose flips the lot to disposed status.
	UpdateStatus(ctx context.Context, l *domain.CableLot) error
}

type CableCutRepository interface {
	ListForLot(ctx context.Context, lotID uuid.UUID, limit, offset int) ([]domain.CableCut, int, error)
	ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.CableCut, error)
}

// =====================================================================
// Consumable batches + consumption log (Wave 117)
// =====================================================================

type ReceiveConsumableBatchInput struct {
	ItemID       uuid.UUID
	BatchNo      string
	TotalQty     int
	ExpiryDate   *time.Time
	SupplierID   *uuid.UUID
	WarehouseID  uuid.UUID
	UnitCost     *float64
	Notes        string
}

type ConsumableBatchListFilter struct {
	ItemID            *uuid.UUID
	WarehouseID       *uuid.UUID
	Status            string
	ExpiringWithinDays *int // when set, only batches with expiry_date within N days
	Limit             int
	Offset            int
}

type ConsumableBatchRepository interface {
	Create(ctx context.Context, b *domain.ConsumableBatch) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ConsumableBatch, error)
	// FindOldestInStock — FIFO pick by received_at. Returns NotFound when
	// no in-stock batch exists for this item.
	FindOldestInStock(ctx context.Context, itemID uuid.UUID) (*domain.ConsumableBatch, error)
	List(ctx context.Context, f ConsumableBatchListFilter) ([]domain.ConsumableBatch, int, error)
	// PersistConsumption atomically decrements remaining_qty + inserts
	// the consumption log row in a single tx.
	PersistConsumption(ctx context.Context, b *domain.ConsumableBatch, log *domain.BatchConsumptionLog) error
	UpdateStatus(ctx context.Context, b *domain.ConsumableBatch) error
}

type BatchConsumptionLogRepository interface {
	ListForBatch(ctx context.Context, batchID uuid.UUID, limit, offset int) ([]domain.BatchConsumptionLog, int, error)
	ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.BatchConsumptionLog, error)
}

// =====================================================================
// Sub-warehouses (Wave 117)
// =====================================================================

type CreateSubWarehouseInput struct {
	ParentWarehouseID uuid.UUID
	Name              string
	Code              string
	OwnerUserID       uuid.UUID
	OwnerRole         domain.SubWarehouseRole
	IsMobile          bool
	VehicleID         string
}

type SubWarehouseListFilter struct {
	ParentWarehouseID *uuid.UUID
	OwnerUserID       *uuid.UUID
	ActiveOnly        bool
}

type SubWarehouseRepository interface {
	Create(ctx context.Context, sw *domain.SubWarehouse) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SubWarehouse, error)
	List(ctx context.Context, f SubWarehouseListFilter) ([]domain.SubWarehouse, error)
	Update(ctx context.Context, sw *domain.SubWarehouse) error
}

// =====================================================================
// Asset location history (Wave 117)
// =====================================================================

type RecordMovementInput struct {
	AssetID            uuid.UUID
	Kind               domain.MovementKind
	FromWarehouseID    *uuid.UUID
	ToWarehouseID      *uuid.UUID
	FromSubWarehouseID *uuid.UUID
	ToSubWarehouseID   *uuid.UUID
	WOID               *uuid.UUID
	CustomerID         *uuid.UUID
	Reason             string
	LocationLabel      string
	MovedBy            uuid.UUID
}

type AssetLocationHistoryRepository interface {
	// Record persists one movement AND updates the asset's denormalized
	// current_location_id + last_movement_at in a single tx.
	Record(ctx context.Context, mv *domain.LocationMovement) error
	ListForAsset(ctx context.Context, assetID uuid.UUID, limit, offset int) ([]domain.LocationMovement, int, error)
	// CurrentLocation reads warehouse.assets.current_location_id.
	CurrentLocation(ctx context.Context, assetID uuid.UUID) (*uuid.UUID, *time.Time, error)
	// ListInTransitOlderThan returns assets stuck in_transit beyond the
	// threshold. Used by the anomaly-alert ticker (TC-ALT-008).
	ListInTransitOlderThan(ctx context.Context, threshold time.Duration) ([]domain.LocationMovement, error)
}

// =====================================================================
// Opname tablet sessions (Wave 117)
// =====================================================================

type CreateOpnameTabletSessionInput struct {
	OpnameSessionID  uuid.UUID
	DeviceID         string
	TechnicianUserID uuid.UUID
}

type OpnameTabletSyncPayload struct {
	SessionID    uuid.UUID
	PayloadHash  string
	TotalScans   int
	// Raw payload bytes are persisted opaque — the reconcile step decodes
	// them. Keeps the sync path indifferent to schema changes in the
	// payload format.
	PayloadBytes []byte
}

type OpnameTabletSessionRepository interface {
	Create(ctx context.Context, s *domain.OpnameTabletSession) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.OpnameTabletSession, error)
	// FindByPayloadHash returns the existing session for an
	// (opname_session_id, payload_hash) — used for idempotent sync.
	FindByPayloadHash(ctx context.Context, opnameSessionID uuid.UUID, hash string) (*domain.OpnameTabletSession, error)
	ListForOpnameSession(ctx context.Context, opnameSessionID uuid.UUID) ([]domain.OpnameTabletSession, error)
	UpdateStatus(ctx context.Context, s *domain.OpnameTabletSession) error
}

// =====================================================================
// QR code generator port (Wave 117)
// =====================================================================

type QRGenerateInput struct {
	ItemType domain.ItemType
	ItemID   string
	Serial   string
}

type QRScanResult struct {
	ItemType domain.ItemType
	ItemID   uuid.UUID
	Asset    *domain.Asset    // populated for Type 1 / Type 4 scans
	Item     *domain.StockItem // catalog row
	Raw      string
}

// QRCodeGenerator — driving-port the usecase consumes. The deterministic
// in-process implementation lives in domain.GenerateQR; tests can swap a
// mock for asserting payload contents without parsing the format.
type QRCodeGenerator interface {
	Generate(in QRGenerateInput) string
	Parse(scanned string) (*domain.QRPayload, error)
}

// =====================================================================
// Netdev bridge port (Wave 117 — Type 4 dispatch cross-context)
// =====================================================================

// NetdevDeviceWriter is the thin driving-port the warehouse usecase
// calls when a Type 4 (network infra) asset is dispatched. The default
// SQL-only adapter lives in adapter/postgres/netdev_bridge.go writing
// directly to netdev.devices; an HTTP adapter could replace it cleanly
// when netdev is hosted in its own binary.
type NetdevDeviceWriter interface {
	// RegisterDevice creates or upserts a netdev.devices row for the
	// given asset + warehouse. Idempotent on serial_no.
	RegisterDevice(ctx context.Context, in RegisterNetdevInput) error
}

type RegisterNetdevInput struct {
	SerialNo      string
	MACAddr       string
	AssetTag      string
	Kind          string
	Model         string
	Manufacturer  string
	WarehouseID   *uuid.UUID
	CustomerID    *uuid.UUID
}
