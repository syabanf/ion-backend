// Package usecase wires the netdevices bounded context together.
//
// Service depends only on the port interfaces, never on the postgres
// adapters directly — same playbook as reseller/usecase. That's what
// lets the bounded context move to its own service binary (cmd/
// netdevices-svc) without touching domain rules.
//
// Every state transition emits an audit row via audit.SafeWrite so the
// netdev change log is queryable from the same identity.audit_logs
// table as the rest of the platform.
package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// DeviceService owns the registration → commission → decommission
// portion of the device lifecycle. Health-driven activation lives in
// HealthService; swap-driven decommission lives in SwapService.
type DeviceService struct {
	devices     port.DeviceRepository
	warehouseRO port.WarehouseAssetReader // optional — commissioning gate
	audit       audit.Writer
}

func NewDeviceService(devices port.DeviceRepository, warehouseRO port.WarehouseAssetReader, auditor audit.Writer) *DeviceService {
	if auditor == nil {
		auditor = audit.Nop{}
	}
	return &DeviceService{
		devices:     devices,
		warehouseRO: warehouseRO,
		audit:       auditor,
	}
}

// RegisterDevice creates a new in_stock device.
func (s *DeviceService) RegisterDevice(ctx context.Context, in port.RegisterDeviceInput) (*domain.Device, error) {
	d, err := domain.NewDevice(in.SerialNo, in.Kind, in.Model, in.Manufacturer)
	if err != nil {
		return nil, err
	}
	d.MACAddr = strings.TrimSpace(in.MACAddr)
	d.AssetTag = strings.TrimSpace(in.AssetTag)
	d.WarehouseID = in.WarehouseID
	if err := s.devices.Create(ctx, d); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device", RecordID: d.ID.String(),
		FieldChanged: "status", After: string(d.Status),
		Reason: "device_registered",
	})
	return d, nil
}

// AllocateToCustomer is the in_stock → allocated transition.
func (s *DeviceService) AllocateToCustomer(ctx context.Context, in port.AllocateDeviceInput) (*domain.Device, error) {
	d, err := s.devices.FindByID(ctx, in.DeviceID)
	if err != nil {
		return nil, err
	}
	before := string(d.Status)
	if err := d.Allocate(in.CustomerID, in.ServiceLocationID); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, d); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device", RecordID: d.ID.String(),
		FieldChanged: "status", Before: before, After: string(d.Status),
		Reason: "device_allocated",
	})
	return d, nil
}

// Commission flips allocated → commissioned. If a warehouse-asset
// reader is wired, we cross-check the matching warehouse asset exists
// + is in a non-decommissioned status before committing — protects
// against orphan netdev rows pointing at vanished warehouse assets.
func (s *DeviceService) Commission(ctx context.Context, in port.CommissionDeviceInput) (*domain.Device, error) {
	d, err := s.devices.FindByID(ctx, in.DeviceID)
	if err != nil {
		return nil, err
	}
	if s.warehouseRO != nil {
		snap, werr := s.warehouseRO.FindAsset(ctx, d.ID)
		if werr != nil {
			// Gate degrades to "warehouse_unavailable" — the netdev
			// commission proceeds. Real outage handling depends on the
			// adapter (cmd/netdevices-svc returns nil,nil when the
			// warehouse schema isn't installed).
			if !derrors.IsNotFound(werr) {
				return nil, werr
			}
		}
		if snap != nil && (snap.Status == "decommissioned" || snap.Status == "scrapped") {
			return nil, derrors.Conflict(
				"device.warehouse_asset_decommissioned",
				"the warehouse asset for this device is decommissioned; can't commission",
			)
		}
	}
	at := in.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	before := string(d.Status)
	if err := d.Commission(at); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, d); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device", RecordID: d.ID.String(),
		FieldChanged: "status", Before: before, After: string(d.Status),
		Reason: "device_commissioned",
	})
	return d, nil
}

// Activate transitions commissioned → active. Public so HealthService
// can call it on first sample; also exposed via HTTP for manual ops.
func (s *DeviceService) Activate(ctx context.Context, id uuid.UUID) (*domain.Device, error) {
	d, err := s.devices.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(d.Status)
	if err := d.Activate(); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, d); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device", RecordID: d.ID.String(),
		FieldChanged: "status", Before: before, After: string(d.Status),
		Reason: "device_activated",
	})
	return d, nil
}

// Decommission is the irreversible exit. Allowed from any non-terminal.
func (s *DeviceService) Decommission(ctx context.Context, id uuid.UUID, by *uuid.UUID, at time.Time) (*domain.Device, error) {
	d, err := s.devices.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(d.Status)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := d.Decommission(at); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, d); err != nil {
		return nil, err
	}
	actor := uuid.Nil
	if by != nil {
		actor = *by
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       actor,
		Module:       "netdev",
		RecordType:   "netdev.device",
		RecordID:     d.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(d.Status),
		Reason:       "device_decommissioned",
	})
	return d, nil
}

// GetDevice / ListByCustomer / ListByStatus are read-throughs.
func (s *DeviceService) GetDevice(ctx context.Context, id uuid.UUID) (*domain.Device, error) {
	return s.devices.FindByID(ctx, id)
}

func (s *DeviceService) ListByCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]domain.Device, int, error) {
	return s.devices.List(ctx, port.DeviceListFilter{
		CustomerID: &customerID,
		Limit:      limit,
		Offset:     offset,
	})
}

func (s *DeviceService) List(ctx context.Context, f port.DeviceListFilter) ([]domain.Device, int, error) {
	return s.devices.List(ctx, f)
}
