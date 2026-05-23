// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the netdevices bounded context.
//
// Hexagonal layout — HTTP handlers depend on UseCase interfaces; the
// UseCase depends on repository + cross-context bridge interfaces;
// postgres adapters implement the repository interfaces; the
// netdevices-svc main wires the cross-context bridges (warehouse asset
// reader, retrofit trigger, work-order creator, device mgmt client) so
// the netdevices code never imports from internal/warehouse,
// internal/field, or any other bounded context.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
)

// =====================================================================
// Filter + input shapes
// =====================================================================

// DeviceListFilter — zero values disable that filter. Limit/Offset are
// applied after the WHERE clause; CountTotal=true populates the total
// for paginated UIs (we don't make every caller pay for it).
type DeviceListFilter struct {
	Status      string
	Kind        string
	CustomerID  *uuid.UUID
	WarehouseID *uuid.UUID
	SerialLike  string // ILIKE %x% search (admin only)
	Limit       int
	Offset      int
}

type RegisterDeviceInput struct {
	SerialNo     string
	MACAddr      string
	AssetTag     string
	Kind         domain.DeviceKind
	Model        string
	Manufacturer string
	WarehouseID  *uuid.UUID
}

type AllocateDeviceInput struct {
	DeviceID          uuid.UUID
	CustomerID        uuid.UUID
	ServiceLocationID uuid.UUID // may be uuid.Nil
}

type CommissionDeviceInput struct {
	DeviceID         uuid.UUID
	TechnicianUserID uuid.UUID
	At               time.Time
}

type ScheduleUpgradeInput struct {
	DeviceID         uuid.UUID
	TargetFirmwareID *uuid.UUID
	ScheduledAt      time.Time
	CreatedBy        *uuid.UUID
}

type RequestSwapInput struct {
	CustomerID     uuid.UUID
	FaultyDeviceID uuid.UUID
	Reason         string
	FaultEventID   *uuid.UUID
	RequestedBy    *uuid.UUID
}

type OpenRMAInput struct {
	DeviceID  uuid.UUID
	Vendor    string
	Reason    string
	CreatedBy *uuid.UUID
}

type RecordHealthInput struct {
	DeviceID      uuid.UUID
	SnappedAt     time.Time
	UptimeSeconds *int64
	SignalDBM     *float64
	PacketLossPct *float64
	CPUPct        *float64
	MemoryPct     *float64
	RawPayload    []byte
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================

type DeviceRepository interface {
	Create(ctx context.Context, d *domain.Device) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Device, error)
	FindBySerial(ctx context.Context, serialNo string) (*domain.Device, error)
	UpdateLifecycle(ctx context.Context, d *domain.Device) error
	List(ctx context.Context, f DeviceListFilter) ([]domain.Device, int, error)
	// FindFirstAvailable picks one in-stock device matching kind+model
	// from the warehouse (FIFO by created_at). Used by the swap stager
	// to allocate a replacement without the caller hard-coding a serial.
	FindFirstAvailable(ctx context.Context, kind domain.DeviceKind, model string, warehouseID *uuid.UUID) (*domain.Device, error)
}

type FirmwareVersionRepository interface {
	Create(ctx context.Context, v *domain.FirmwareVersion) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.FirmwareVersion, error)
	FindRecommended(ctx context.Context, kind domain.DeviceKind, model string) (*domain.FirmwareVersion, error)
	List(ctx context.Context, kind, model string) ([]domain.FirmwareVersion, error)
}

type FirmwareUpgradeJobRepository interface {
	Create(ctx context.Context, j *domain.FirmwareUpgradeJob) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.FirmwareUpgradeJob, error)
	UpdateLifecycle(ctx context.Context, j *domain.FirmwareUpgradeJob) error
	ListPendingForDevice(ctx context.Context, deviceID uuid.UUID) ([]domain.FirmwareUpgradeJob, error)
}

type DeviceSwapRepository interface {
	Create(ctx context.Context, s *domain.DeviceSwap) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.DeviceSwap, error)
	UpdateLifecycle(ctx context.Context, s *domain.DeviceSwap) error
	List(ctx context.Context, status string, customerID *uuid.UUID, limit, offset int) ([]domain.DeviceSwap, int, error)
}

type RMARepository interface {
	Create(ctx context.Context, r *domain.RMARecord) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.RMARecord, error)
	UpdateLifecycle(ctx context.Context, r *domain.RMARecord) error
	ListByStatus(ctx context.Context, status string, limit, offset int) ([]domain.RMARecord, int, error)
	// ListExpirable returns RMA records past the 90d freshness window
	// for the cron to flip into RMAStatusExpired.
	ListExpirable(ctx context.Context, now time.Time) ([]domain.RMARecord, error)
}

type HealthSnapshotRepository interface {
	Insert(ctx context.Context, s *domain.HealthSnapshot) error
	ListRecent(ctx context.Context, deviceID uuid.UUID, limit int) ([]domain.HealthSnapshot, error)
	// CountConsecutiveLowScores returns how many of the most-recent N
	// snapshots scored below `threshold`. Powers the degradation
	// watcher inside HealthService.RecordSnapshot.
	CountConsecutiveLowScores(ctx context.Context, deviceID uuid.UUID, threshold, lookback int) (int, error)
}

type ComplianceRepository interface {
	Create(ctx context.Context, r *domain.FirmwareComplianceRun) error
	UpdateFinish(ctx context.Context, r *domain.FirmwareComplianceRun) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.FirmwareComplianceRun, error)
}

// =====================================================================
// Cross-context bridges (narrow ports — implemented in cmd/netdevices-svc)
// =====================================================================

// WarehouseAssetSnapshot is the bare-minimum projection the
// commissioning flow needs from warehouse. Kept tiny so a real
// extraction can satisfy it without dragging the full warehouse domain
// across the wire.
type WarehouseAssetSnapshot struct {
	AssetID     uuid.UUID
	StockItemID uuid.UUID
	WarehouseID *uuid.UUID
	Status      string
	SerialNo    string
}

// WarehouseAssetReader is the read-only bridge to warehouse.assets used
// when commissioning a device — we verify the matching warehouse row
// exists + is in a non-decommissioned status before we let the netdev
// device transition to commissioned. The bridge MUST be implemented in
// cmd/netdevices-svc (not in this package) — see Wave 113 coordination
// note: NO direct imports from internal/warehouse.
type WarehouseAssetReader interface {
	FindAsset(ctx context.Context, deviceID uuid.UUID) (*WarehouseAssetSnapshot, error)
}

// RetrofitTrigger bridges to warehouse Asset Retrofit (Wave 87) when a
// device swap completes. We don't make the swap atomic with the
// retrofit — the swap stays at 'swapped' even if the retrofit call
// fails, and the operator can retry the retrofit via a follow-up
// endpoint (parity with the customer-PO accept fan-out semantics).
type RetrofitTrigger interface {
	CreateRetrofitForSwap(ctx context.Context, swapID, oldDeviceID, newDeviceID uuid.UUID) (retrofitID uuid.UUID, err error)
}

// WorkOrderCreator bridges to the field WO context. AssignTechnician on
// a swap goes through here to create the install/swap WO that the
// technician picks up on mobile.
type WorkOrderCreator interface {
	CreateSwapWO(ctx context.Context, swapID, customerID, technicianID uuid.UUID) (woID uuid.UUID, err error)
}

// DeviceMgmtClient is the vendor-SDK adapter. Wave 113 ships only a
// stub (returns nil from every method) — real SNMP/NETCONF lands when
// DEVICE_MGMT_ENABLED=true and the polling pipeline is wired.
type DeviceMgmtClient interface {
	ScheduleFirmwareUpgrade(ctx context.Context, device *domain.Device, targetVersion string) error
	PushStagedImage(ctx context.Context, device *domain.Device, version string) error
	TriggerUpgrade(ctx context.Context, device *domain.Device) error
	RollbackFirmware(ctx context.Context, device *domain.Device, previousVersion string) error
}
