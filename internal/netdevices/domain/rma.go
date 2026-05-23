package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// RMAStatus tracks the vendor-return workflow.
//
// Legal transitions (enforced below):
//
//	open → shipped → received → replaced → closed
//	                         ↘ rejected → closed
//	any-non-closed → expired (after 90d untouched; driven by cron, not
//	                          a user action)
type RMAStatus string

const (
	RMAStatusOpen     RMAStatus = "open"
	RMAStatusShipped  RMAStatus = "shipped"
	RMAStatusReceived RMAStatus = "received"
	RMAStatusReplaced RMAStatus = "replaced"
	RMAStatusRejected RMAStatus = "rejected"
	RMAStatusClosed   RMAStatus = "closed"
	RMAStatusExpired  RMAStatus = "expired"
)

// RMARecord is the per-device RMA ticket. The device itself stays at
// DeviceStatusRMAOpen until MarkReturned is called on the device — the
// RMA row tracks the vendor-side workflow independently so a shipping
// delay doesn't block other lifecycle actions on the device.
type RMARecord struct {
	ID                uuid.UUID
	DeviceID          uuid.UUID
	Vendor            string
	VendorRMANo       string
	ReturnReason      string
	ShippedAt         *time.Time
	ReceivedAt        *time.Time
	ReplacementSerial string
	Status            RMAStatus
	Notes             string
	CreatedBy         *uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewRMARecord constructs an open RMA. Return reason is required so the
// vendor portal has something to display.
func NewRMARecord(deviceID uuid.UUID, vendor, reason string, createdBy *uuid.UUID) (*RMARecord, error) {
	if deviceID == uuid.Nil {
		return nil, errors.Validation("rma.device_required", "device_id is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.Validation("rma.reason_required", "return_reason is required")
	}
	now := time.Now().UTC()
	return &RMARecord{
		ID:           uuid.New(),
		DeviceID:     deviceID,
		Vendor:       strings.TrimSpace(vendor),
		ReturnReason: reason,
		Status:       RMAStatusOpen,
		CreatedBy:    createdBy,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// MarkShipped flips open → shipped with the vendor's RMA number snapshot.
func (r *RMARecord) MarkShipped(vendorRMANo string, at time.Time) error {
	if r.Status == RMAStatusShipped {
		return nil
	}
	if r.Status != RMAStatusOpen {
		return errors.Conflict(
			"rma.invalid_state_transition",
			"only open RMAs can be shipped (current: "+string(r.Status)+")",
		)
	}
	r.Status = RMAStatusShipped
	r.VendorRMANo = strings.TrimSpace(vendorRMANo)
	r.ShippedAt = &at
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkReceived records arrival at the vendor. replacementSerial is the
// vendor's pre-allocation of the unit they'll send back — may be empty
// at this point (they'll fill it in when they ship the replacement).
func (r *RMARecord) MarkReceived(replacementSerial string, at time.Time) error {
	if r.Status == RMAStatusReceived {
		return nil
	}
	if r.Status != RMAStatusShipped {
		return errors.Conflict(
			"rma.invalid_state_transition",
			"only shipped RMAs can be marked received (current: "+string(r.Status)+")",
		)
	}
	r.Status = RMAStatusReceived
	r.ReceivedAt = &at
	r.ReplacementSerial = strings.TrimSpace(replacementSerial)
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkReplaced finalises the success path: the vendor sent us a
// replacement. The replacement device is registered separately via
// DeviceService.RegisterDevice — this method just closes the RMA loop.
func (r *RMARecord) MarkReplaced() error {
	if r.Status == RMAStatusReplaced {
		return nil
	}
	if r.Status != RMAStatusReceived {
		return errors.Conflict(
			"rma.invalid_state_transition",
			"only received RMAs can be replaced (current: "+string(r.Status)+")",
		)
	}
	r.Status = RMAStatusReplaced
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkRejected is the failure path: the vendor declined the return.
// reason is captured into Notes so the next reviewer knows why.
func (r *RMARecord) MarkRejected(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("rma.reason_required", "rejection reason is required")
	}
	if r.Status == RMAStatusRejected {
		return nil
	}
	if r.Status != RMAStatusReceived && r.Status != RMAStatusShipped {
		return errors.Conflict(
			"rma.invalid_state_transition",
			"only shipped or received RMAs can be rejected (current: "+string(r.Status)+")",
		)
	}
	r.Status = RMAStatusRejected
	r.Notes = reason
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// Close is the terminal close-out. Allowed from replaced or rejected.
func (r *RMARecord) Close() error {
	if r.Status == RMAStatusClosed {
		return nil
	}
	if r.Status != RMAStatusReplaced && r.Status != RMAStatusRejected {
		return errors.Conflict(
			"rma.invalid_state_transition",
			"only replaced or rejected RMAs can be closed (current: "+string(r.Status)+")",
		)
	}
	r.Status = RMAStatusClosed
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// Expire is the cron-driven escape hatch. An RMA untouched for 90+ days
// gets flipped to expired so dashboards stop alerting on it.
func (r *RMARecord) Expire(now time.Time) (changed bool, err error) {
	if r.Status == RMAStatusClosed || r.Status == RMAStatusExpired {
		return false, nil
	}
	// 90-day window starts at the LAST update — re-shipping resets it.
	if now.Sub(r.UpdatedAt) < 90*24*time.Hour {
		return false, nil
	}
	r.Status = RMAStatusExpired
	r.Notes = "auto-expired after 90 days without update"
	r.UpdatedAt = now
	return true, nil
}
