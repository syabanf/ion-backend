package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 113 — Device state-machine contract tests (TC-NDL-032).
//
// Lifecycle:
//
//	in_stock → allocated → commissioned → active ↔ degraded
//	active|degraded → quarantine
//	any → rma_open → rma_returned
//	any-non-terminal → decommissioned   (terminal)
// =====================================================================

func newDeviceAt(t *testing.T, status DeviceStatus) *Device {
	t.Helper()
	d, err := NewDevice("SN-TEST-001", DeviceKindONT, "TestModel", "TestVendor")
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	now := time.Now().UTC()
	customer := uuid.New()
	location := uuid.New()
	switch status {
	case DeviceStatusInStock:
	case DeviceStatusAllocated:
		if err := d.Allocate(customer, location); err != nil {
			t.Fatalf("Allocate: %v", err)
		}
	case DeviceStatusCommissioned:
		if err := d.Allocate(customer, location); err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if err := d.Commission(now); err != nil {
			t.Fatalf("Commission: %v", err)
		}
	case DeviceStatusActive:
		if err := d.Allocate(customer, location); err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if err := d.Commission(now); err != nil {
			t.Fatalf("Commission: %v", err)
		}
		if err := d.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
	case DeviceStatusDegraded:
		if err := d.Allocate(customer, location); err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if err := d.Commission(now); err != nil {
			t.Fatalf("Commission: %v", err)
		}
		if err := d.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if err := d.MarkDegraded("low signal"); err != nil {
			t.Fatalf("MarkDegraded: %v", err)
		}
	case DeviceStatusQuarantine:
		if err := d.Allocate(customer, location); err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if err := d.Commission(now); err != nil {
			t.Fatalf("Commission: %v", err)
		}
		if err := d.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if err := d.Quarantine("suspicious"); err != nil {
			t.Fatalf("Quarantine: %v", err)
		}
	case DeviceStatusRMAOpen:
		if err := d.OpenRMA(); err != nil {
			t.Fatalf("OpenRMA: %v", err)
		}
	case DeviceStatusRMAReturned:
		if err := d.OpenRMA(); err != nil {
			t.Fatalf("OpenRMA: %v", err)
		}
		if err := d.MarkRMAReturned("REP-001"); err != nil {
			t.Fatalf("MarkRMAReturned: %v", err)
		}
	case DeviceStatusDecommissioned:
		if err := d.Decommission(now); err != nil {
			t.Fatalf("Decommission: %v", err)
		}
	}
	return d
}

func TestDeviceSM_ValidTransitions(t *testing.T) {
	now := time.Now().UTC()
	customer := uuid.New()
	location := uuid.New()

	cases := []struct {
		name       string
		from       DeviceStatus
		action     func(*Device) error
		wantStatus DeviceStatus
	}{
		{"in_stock → allocated", DeviceStatusInStock,
			func(d *Device) error { return d.Allocate(customer, location) }, DeviceStatusAllocated},
		{"allocated → commissioned", DeviceStatusAllocated,
			func(d *Device) error { return d.Commission(now) }, DeviceStatusCommissioned},
		{"commissioned → active", DeviceStatusCommissioned,
			func(d *Device) error { return d.Activate() }, DeviceStatusActive},
		{"active → degraded", DeviceStatusActive,
			func(d *Device) error { return d.MarkDegraded("bad signal") }, DeviceStatusDegraded},
		{"degraded → active (recovery)", DeviceStatusDegraded,
			func(d *Device) error { return d.Activate() }, DeviceStatusActive},
		{"active → quarantine", DeviceStatusActive,
			func(d *Device) error { return d.Quarantine("isolating") }, DeviceStatusQuarantine},
		{"degraded → quarantine", DeviceStatusDegraded,
			func(d *Device) error { return d.Quarantine("isolating") }, DeviceStatusQuarantine},
		{"active → rma_open", DeviceStatusActive,
			func(d *Device) error { return d.OpenRMA() }, DeviceStatusRMAOpen},
		{"in_stock → rma_open (DOA)", DeviceStatusInStock,
			func(d *Device) error { return d.OpenRMA() }, DeviceStatusRMAOpen},
		{"rma_open → rma_returned", DeviceStatusRMAOpen,
			func(d *Device) error { return d.MarkRMAReturned("REP-001") }, DeviceStatusRMAReturned},
		{"active → decommissioned", DeviceStatusActive,
			func(d *Device) error { return d.Decommission(now) }, DeviceStatusDecommissioned},
		{"in_stock → decommissioned", DeviceStatusInStock,
			func(d *Device) error { return d.Decommission(now) }, DeviceStatusDecommissioned},
		// Idempotent destinations
		{"allocated → allocated (same customer)", DeviceStatusAllocated,
			func(d *Device) error {
				return d.Allocate(*d.CustomerID, *d.ServiceLocation)
			}, DeviceStatusAllocated},
		{"commissioned → commissioned", DeviceStatusCommissioned,
			func(d *Device) error { return d.Commission(now) }, DeviceStatusCommissioned},
		{"active → active", DeviceStatusActive,
			func(d *Device) error { return d.Activate() }, DeviceStatusActive},
		{"decommissioned → decommissioned", DeviceStatusDecommissioned,
			func(d *Device) error { return d.Decommission(now) }, DeviceStatusDecommissioned},
		{"rma_open → rma_open (idempotent)", DeviceStatusRMAOpen,
			func(d *Device) error { return d.OpenRMA() }, DeviceStatusRMAOpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeviceAt(t, tc.from)
			if err := tc.action(d); err != nil {
				t.Fatalf("action: %v", err)
			}
			if d.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", d.Status, tc.wantStatus)
			}
		})
	}
}

func TestDeviceSM_InvalidTransitions(t *testing.T) {
	now := time.Now().UTC()
	customer := uuid.New()
	location := uuid.New()
	otherCustomer := uuid.New()

	cases := []struct {
		name     string
		from     DeviceStatus
		action   func(*Device) error
		wantCode string
	}{
		// Cannot resurrect a decommissioned device.
		{"decommissioned → allocated", DeviceStatusDecommissioned,
			func(d *Device) error { return d.Allocate(customer, location) },
			"device.invalid_state_transition"},
		{"decommissioned → activate", DeviceStatusDecommissioned,
			func(d *Device) error { return d.Activate() },
			"device.invalid_state_transition"},
		{"decommissioned → rma_open", DeviceStatusDecommissioned,
			func(d *Device) error { return d.OpenRMA() },
			"device.invalid_state_transition"},
		// Skip allocation: in_stock cannot commission directly.
		{"in_stock → commissioned (skip allocate)", DeviceStatusInStock,
			func(d *Device) error { return d.Commission(now) },
			"device.invalid_state_transition"},
		// Cannot re-allocate to a different customer.
		{"allocated → allocated (different customer)", DeviceStatusAllocated,
			func(d *Device) error { return d.Allocate(otherCustomer, location) },
			"device.allocated_to_other"},
		// active cannot mark returned without going through rma_open.
		{"active → rma_returned (skip rma_open)", DeviceStatusActive,
			func(d *Device) error { return d.MarkRMAReturned("REP-002") },
			"device.invalid_state_transition"},
		// quarantine has no direct reverse — must be re-activated through specific path
		{"quarantine → activate (not allowed)", DeviceStatusQuarantine,
			func(d *Device) error { return d.Activate() },
			"device.invalid_state_transition"},
		// Reason validation
		{"degraded without reason", DeviceStatusActive,
			func(d *Device) error { return d.MarkDegraded("") },
			"device.reason_required"},
		{"quarantine without reason", DeviceStatusActive,
			func(d *Device) error { return d.Quarantine("   ") },
			"device.reason_required"},
		// Allocate validation
		{"allocate without customer", DeviceStatusInStock,
			func(d *Device) error { return d.Allocate(uuid.Nil, location) },
			"device.customer_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeviceAt(t, tc.from)
			err := tc.action(d)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", d.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("err type: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", de.Code, tc.wantCode)
			}
		})
	}
}

func TestNewDevice_Validation(t *testing.T) {
	if _, err := NewDevice("  ", DeviceKindONT, "M", "V"); err == nil {
		t.Fatalf("empty serial should error")
	}
	if _, err := NewDevice("SN-1", DeviceKind("space-alien"), "M", "V"); err == nil {
		t.Fatalf("invalid kind should error")
	}
}
