package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SwapStatus tracks the swap workflow.
//
// Legal transitions (enforced below):
//
//	requested → approved → staged → technician_assigned → swapped → closed
//	                                                              ↘ rolled_back (recovery)
type SwapStatus string

const (
	SwapStatusRequested           SwapStatus = "requested"
	SwapStatusApproved            SwapStatus = "approved"
	SwapStatusStaged              SwapStatus = "staged"
	SwapStatusTechnicianAssigned  SwapStatus = "technician_assigned"
	SwapStatusSwapped             SwapStatus = "swapped"
	SwapStatusRolledBack          SwapStatus = "rolled_back"
	SwapStatusClosed              SwapStatus = "closed"
)

// DeviceSwap is the orchestrator entity. It links the faulty device,
// the replacement, the work order created for the technician, and the
// retrofit row produced when the swap completes — bridging the netdev
// context to warehouse Wave 87 + field WO context via narrow ports.
type DeviceSwap struct {
	ID                  uuid.UUID
	CustomerID          uuid.UUID
	FaultyDeviceID      uuid.UUID
	ReplacementDeviceID *uuid.UUID
	Reason              string
	FaultEventID        *uuid.UUID
	Status              SwapStatus
	WOID                *uuid.UUID
	TechnicianUserID    *uuid.UUID
	SwapStartedAt       *time.Time
	SwapCompletedAt     *time.Time
	RetrofitID          *uuid.UUID
	RequestedBy         *uuid.UUID
	ApprovedBy          *uuid.UUID
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// NewDeviceSwap creates a swap in the Requested state. Reason is
// required so the approval reviewer sees WHY without chasing tickets.
func NewDeviceSwap(customerID, faultyDeviceID uuid.UUID, reason string, faultEventID *uuid.UUID, requestedBy *uuid.UUID) (*DeviceSwap, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("swap.customer_required", "customer_id is required")
	}
	if faultyDeviceID == uuid.Nil {
		return nil, errors.Validation("swap.faulty_device_required", "faulty_device_id is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.Validation("swap.reason_required", "reason is required")
	}
	now := time.Now().UTC()
	return &DeviceSwap{
		ID:             uuid.New(),
		CustomerID:     customerID,
		FaultyDeviceID: faultyDeviceID,
		Reason:         reason,
		FaultEventID:   faultEventID,
		Status:         SwapStatusRequested,
		RequestedBy:    requestedBy,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Approve flips requested → approved and snapshots the approver.
// Idempotent on already-approved.
func (s *DeviceSwap) Approve(by uuid.UUID) error {
	if s.Status == SwapStatusApproved {
		return nil
	}
	if s.Status != SwapStatusRequested {
		return errors.Conflict(
			"swap.invalid_state_transition",
			"only requested swaps can be approved (current: "+string(s.Status)+")",
		)
	}
	s.Status = SwapStatusApproved
	if by != uuid.Nil {
		s.ApprovedBy = &by
	}
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// Stage binds the replacement device to the swap. The usecase allocates
// the replacement from stock first; this method just records the link.
func (s *DeviceSwap) Stage(replacementDeviceID uuid.UUID) error {
	if replacementDeviceID == uuid.Nil {
		return errors.Validation("swap.replacement_required", "replacement_device_id is required")
	}
	if s.Status == SwapStatusStaged {
		if s.ReplacementDeviceID != nil && *s.ReplacementDeviceID == replacementDeviceID {
			return nil // idempotent
		}
		return errors.Conflict(
			"swap.replacement_already_set",
			"swap already has a different replacement bound",
		)
	}
	if s.Status != SwapStatusApproved {
		return errors.Conflict(
			"swap.invalid_state_transition",
			"only approved swaps can be staged (current: "+string(s.Status)+")",
		)
	}
	s.Status = SwapStatusStaged
	s.ReplacementDeviceID = &replacementDeviceID
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// AssignTechnician transitions staged → technician_assigned. The usecase
// then calls the WorkOrderCreator port and stamps the resulting WO id.
func (s *DeviceSwap) AssignTechnician(technicianUserID uuid.UUID, woID *uuid.UUID) error {
	if technicianUserID == uuid.Nil {
		return errors.Validation("swap.technician_required", "technician_user_id is required")
	}
	if s.Status == SwapStatusTechnicianAssigned {
		return nil
	}
	if s.Status != SwapStatusStaged {
		return errors.Conflict(
			"swap.invalid_state_transition",
			"only staged swaps can be assigned (current: "+string(s.Status)+")",
		)
	}
	s.Status = SwapStatusTechnicianAssigned
	s.TechnicianUserID = &technicianUserID
	s.WOID = woID
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// Complete records the physical swap-out in the field. The usecase then
// pivots to: faulty.Decommission(), replacement.Activate(), and calls
// RetrofitTrigger to record the warehouse-side consume/produce pair.
func (s *DeviceSwap) Complete(retrofitID *uuid.UUID, at time.Time) error {
	if s.Status == SwapStatusSwapped {
		return nil
	}
	if s.Status != SwapStatusTechnicianAssigned {
		return errors.Conflict(
			"swap.invalid_state_transition",
			"only technician_assigned swaps can be completed (current: "+string(s.Status)+")",
		)
	}
	s.Status = SwapStatusSwapped
	s.SwapCompletedAt = &at
	s.RetrofitID = retrofitID
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// RollBack is the recovery path when something goes wrong post-swap
// (e.g. the replacement won't come online). Only allowed from Swapped.
// The caller is responsible for the device-state rollback (re-activate
// the faulty, deallocate the replacement) — this method just flips the
// swap header.
func (s *DeviceSwap) RollBack(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("swap.reason_required", "rollback reason is required")
	}
	if s.Status == SwapStatusRolledBack {
		return nil
	}
	if s.Status != SwapStatusSwapped {
		return errors.Conflict(
			"swap.invalid_state_transition",
			"only swapped records can be rolled back (current: "+string(s.Status)+")",
		)
	}
	s.Status = SwapStatusRolledBack
	s.Reason = reason
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// Close finalises a successful swap. Idempotent on already-closed.
func (s *DeviceSwap) Close() error {
	if s.Status == SwapStatusClosed {
		return nil
	}
	if s.Status != SwapStatusSwapped {
		return errors.Conflict(
			"swap.invalid_state_transition",
			"only swapped records can be closed (current: "+string(s.Status)+")",
		)
	}
	s.Status = SwapStatusClosed
	s.UpdatedAt = time.Now().UTC()
	return nil
}
