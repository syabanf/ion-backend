// Package domain holds the netdevices bounded context entities and
// value objects.
//
// Rules (same as reseller / partnership / vendormgmt domains):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP adapter can map
//     them to the right HTTP status without inspecting strings.
//   - State machines live here, never in the SQL layer. The DB CHECK
//     clauses are belt-and-suspenders for manual hot-patches.
//
// Wave 113 scope: Device, FirmwareVersion + FirmwareUpgradeJob,
// DeviceSwap, RMARecord, HealthSnapshot, FirmwareComplianceRun. Together
// they cover the 32 TC-NDL-* test cases.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// DeviceKind enumerates the hardware classes the netdev module knows
// about. The DB CHECK in migration 0076 mirrors this set — adding a
// new kind requires a paired migration so the constraints stay aligned.
type DeviceKind string

const (
	DeviceKindONT            DeviceKind = "ont"
	DeviceKindOLTPort        DeviceKind = "olt_port"
	DeviceKindRouter         DeviceKind = "router"
	DeviceKindSwitch         DeviceKind = "switch"
	DeviceKindAP             DeviceKind = "ap"
	DeviceKindONU            DeviceKind = "onu"
	DeviceKindONX            DeviceKind = "onx"
	DeviceKindMediaConverter DeviceKind = "mediaconverter"
	DeviceKindOther          DeviceKind = "other"
)

var validKinds = map[DeviceKind]bool{
	DeviceKindONT: true, DeviceKindOLTPort: true, DeviceKindRouter: true,
	DeviceKindSwitch: true, DeviceKindAP: true, DeviceKindONU: true,
	DeviceKindONX: true, DeviceKindMediaConverter: true, DeviceKindOther: true,
}

// DeviceStatus tracks the lifecycle position.
//
// Legal transitions (enforced below):
//
//	in_stock → allocated → commissioned → active ↔ degraded
//	active|degraded → quarantine
//	any → rma_open → rma_returned
//	any-non-terminal → decommissioned   (terminal)
type DeviceStatus string

const (
	DeviceStatusInStock        DeviceStatus = "in_stock"
	DeviceStatusAllocated      DeviceStatus = "allocated"
	DeviceStatusCommissioned   DeviceStatus = "commissioned"
	DeviceStatusActive         DeviceStatus = "active"
	DeviceStatusDegraded       DeviceStatus = "degraded"
	DeviceStatusQuarantine     DeviceStatus = "quarantine"
	DeviceStatusRMAOpen        DeviceStatus = "rma_open"
	DeviceStatusRMAReturned    DeviceStatus = "rma_returned"
	DeviceStatusDecommissioned DeviceStatus = "decommissioned"
)

// Device is the central aggregate — one row per physical unit. Most
// life-cycle methods are idempotent on the destination state so a
// double-click on the UI doesn't 409. Decommission is the one-way exit.
type Device struct {
	ID               uuid.UUID
	SerialNo         string
	MACAddr          string
	AssetTag         string
	Kind             DeviceKind
	Model            string
	Manufacturer     string
	FirmwareVersion  string
	Status           DeviceStatus
	WarehouseID      *uuid.UUID
	CustomerID       *uuid.UUID
	ServiceLocation  *uuid.UUID
	IPAddress        string
	MgmtURI          string
	LastSeenAt       *time.Time
	CommissionedAt   *time.Time
	DecommissionedAt *time.Time
	Notes            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewDevice constructs a fresh in-stock unit. Serial number is the
// canonical identity — empty is a hard validation error so we never
// accept a device the warehouse can't ship back to.
func NewDevice(serialNo string, kind DeviceKind, model, manufacturer string) (*Device, error) {
	serialNo = strings.TrimSpace(serialNo)
	if serialNo == "" {
		return nil, errors.Validation("device.serial_required", "serial_no is required")
	}
	if !validKinds[kind] {
		return nil, errors.Validation("device.kind_invalid", "device kind is not a known value")
	}
	now := time.Now().UTC()
	return &Device{
		ID:           uuid.New(),
		SerialNo:     serialNo,
		Kind:         kind,
		Model:        strings.TrimSpace(model),
		Manufacturer: strings.TrimSpace(manufacturer),
		Status:       DeviceStatusInStock,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Allocate reserves an in-stock device for a customer + service
// location, but doesn't physically install it yet. Idempotent on
// already-allocated when the customer matches; rejects mismatches so a
// stray allocation can't silently rebind to a different customer.
func (d *Device) Allocate(customerID, serviceLocationID uuid.UUID) error {
	if customerID == uuid.Nil {
		return errors.Validation("device.customer_required", "customer_id is required to allocate")
	}
	switch d.Status {
	case DeviceStatusAllocated:
		if d.CustomerID != nil && *d.CustomerID == customerID {
			return nil // idempotent
		}
		return errors.Conflict(
			"device.allocated_to_other",
			"device is already allocated to a different customer",
		)
	case DeviceStatusInStock:
		// happy path
	default:
		return errors.Conflict(
			"device.invalid_state_transition",
			"only in_stock devices can be allocated (current: "+string(d.Status)+")",
		)
	}
	d.Status = DeviceStatusAllocated
	d.CustomerID = &customerID
	if serviceLocationID != uuid.Nil {
		d.ServiceLocation = &serviceLocationID
	}
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// Commission flips an allocated device to commissioned and snapshots the
// install time. Idempotent on already-commissioned; rejects every other
// state.
func (d *Device) Commission(at time.Time) error {
	if d.Status == DeviceStatusCommissioned ||
		d.Status == DeviceStatusActive ||
		d.Status == DeviceStatusDegraded {
		return nil // idempotent — already past the commission line
	}
	if d.Status != DeviceStatusAllocated {
		return errors.Conflict(
			"device.invalid_state_transition",
			"only allocated devices can be commissioned (current: "+string(d.Status)+")",
		)
	}
	d.Status = DeviceStatusCommissioned
	d.CommissionedAt = &at
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// Activate flips a commissioned (or degraded → active recovery) device
// into the steady operating state. The HealthService calls this on the
// first health sample so a device with no telemetry sticks at
// commissioned until it reports.
func (d *Device) Activate() error {
	if d.Status == DeviceStatusActive {
		return nil
	}
	if d.Status != DeviceStatusCommissioned && d.Status != DeviceStatusDegraded {
		return errors.Conflict(
			"device.invalid_state_transition",
			"only commissioned or degraded devices can be activated (current: "+string(d.Status)+")",
		)
	}
	d.Status = DeviceStatusActive
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkDegraded is the entry point from the health monitor: 3 bad
// snapshots in a row → degraded. Reason is required so NOC sees WHY.
func (d *Device) MarkDegraded(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("device.reason_required", "reason is required for degraded transition")
	}
	if d.Status == DeviceStatusDegraded {
		return nil
	}
	if d.Status != DeviceStatusActive {
		return errors.Conflict(
			"device.invalid_state_transition",
			"only active devices can be marked degraded (current: "+string(d.Status)+")",
		)
	}
	d.Status = DeviceStatusDegraded
	d.Notes = reason
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// Quarantine isolates a device suspected of misbehaving. Allowed from
// active or degraded — anything else means the device isn't operational
// yet so quarantine is meaningless.
func (d *Device) Quarantine(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("device.reason_required", "reason is required for quarantine")
	}
	if d.Status == DeviceStatusQuarantine {
		return nil
	}
	if d.Status != DeviceStatusActive && d.Status != DeviceStatusDegraded {
		return errors.Conflict(
			"device.invalid_state_transition",
			"only active or degraded devices can be quarantined (current: "+string(d.Status)+")",
		)
	}
	d.Status = DeviceStatusQuarantine
	d.Notes = reason
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// OpenRMA is the entry point when a device is being shipped back to the
// vendor. Allowed from any non-decommissioned state so an in-stock
// dead-on-arrival can be RMA'd straight away.
func (d *Device) OpenRMA() error {
	if d.Status == DeviceStatusRMAOpen {
		return nil
	}
	if d.Status == DeviceStatusDecommissioned {
		return errors.Conflict(
			"device.invalid_state_transition",
			"decommissioned devices can't be opened for RMA",
		)
	}
	d.Status = DeviceStatusRMAOpen
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkRMAReturned flips a device from rma_open to rma_returned with the
// replacement serial captured for the audit trail.
func (d *Device) MarkRMAReturned(replacementSerial string) error {
	if d.Status == DeviceStatusRMAReturned {
		return nil
	}
	if d.Status != DeviceStatusRMAOpen {
		return errors.Conflict(
			"device.invalid_state_transition",
			"only rma_open devices can be marked returned (current: "+string(d.Status)+")",
		)
	}
	d.Status = DeviceStatusRMAReturned
	d.Notes = "rma_returned; replacement_serial=" + strings.TrimSpace(replacementSerial)
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// Decommission is the irreversible exit. Allowed from any non-terminal
// state; idempotent on already-decommissioned.
func (d *Device) Decommission(at time.Time) error {
	if d.Status == DeviceStatusDecommissioned {
		return nil
	}
	d.Status = DeviceStatusDecommissioned
	d.DecommissionedAt = &at
	d.UpdatedAt = time.Now().UTC()
	return nil
}

// IsOperational reports whether the device is in a steady-state ready
// to serve traffic. Used by the firmware compliance scanner to decide
// which devices to evaluate.
func (d *Device) IsOperational() bool {
	return d.Status == DeviceStatusActive || d.Status == DeviceStatusDegraded
}
