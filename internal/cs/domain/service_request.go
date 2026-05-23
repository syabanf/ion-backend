package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Service Request — first-class entity linked 1:1 to a ticket
// =====================================================================

// ServiceRequestType matches the cs.service_requests.request_type CHECK.
type ServiceRequestType string

const (
	SRTypePlanChange         ServiceRequestType = "plan_change"
	SRTypeAddressRelocation  ServiceRequestType = "address_relocation"
	SRTypeAddOn              ServiceRequestType = "add_on"
	SRTypeSuspendPause       ServiceRequestType = "suspend_pause"
	SRTypeSpeedUpgrade       ServiceRequestType = "speed_upgrade"
	SRTypeSpeedDowngrade     ServiceRequestType = "speed_downgrade"
	SRTypeEquipmentSwap      ServiceRequestType = "equipment_swap"
	SRTypeVacationHold       ServiceRequestType = "vacation_hold"
	SRTypeData               ServiceRequestType = "data"
	SRTypeOther              ServiceRequestType = "other"
)

func (r ServiceRequestType) Valid() bool {
	switch r {
	case SRTypePlanChange, SRTypeAddressRelocation, SRTypeAddOn,
		SRTypeSuspendPause, SRTypeSpeedUpgrade, SRTypeSpeedDowngrade,
		SRTypeEquipmentSwap, SRTypeVacationHold, SRTypeData, SRTypeOther:
		return true
	}
	return false
}

// RequiresApproval returns whether this request type needs supervisor
// sign-off. Lock-in-period sensitive types (downgrade / suspend /
// vacation) need approval; speed-upgrade and add-on auto-flow.
//
// Per PRD §17 Q2 — plan downgrade requires Sales Manager approval.
func (r ServiceRequestType) RequiresApproval() bool {
	switch r {
	case SRTypeSpeedDowngrade, SRTypeSuspendPause, SRTypeVacationHold,
		SRTypePlanChange, SRTypeAddressRelocation:
		return true
	}
	return false
}

// ServiceRequestStatus tracks the lifecycle:
//
//	submitted → approved | rejected
//	approved → in_progress → fulfilled | cancelled
//	(rejected and cancelled are terminal)
type ServiceRequestStatus string

const (
	SRStatusSubmitted  ServiceRequestStatus = "submitted"
	SRStatusApproved   ServiceRequestStatus = "approved"
	SRStatusRejected   ServiceRequestStatus = "rejected"
	SRStatusInProgress ServiceRequestStatus = "in_progress"
	SRStatusFulfilled  ServiceRequestStatus = "fulfilled"
	SRStatusCancelled  ServiceRequestStatus = "cancelled"
)

func (s ServiceRequestStatus) Valid() bool {
	switch s {
	case SRStatusSubmitted, SRStatusApproved, SRStatusRejected,
		SRStatusInProgress, SRStatusFulfilled, SRStatusCancelled:
		return true
	}
	return false
}

// ServiceRequest is the aggregate.
type ServiceRequest struct {
	ID                  uuid.UUID
	TicketID            uuid.UUID
	CustomerID          uuid.UUID
	RequestType         ServiceRequestType
	ReferenceID         *uuid.UUID
	Status              ServiceRequestStatus
	SubmittedBy         *uuid.UUID
	ApprovedBy          *uuid.UUID
	ApprovalDecisionAt  *time.Time
	RejectionReason     string
	FulfilledAt         *time.Time
	CancelledReason     string
	SLADueAt            *time.Time
	Payload             map[string]any
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// NewServiceRequest constructs a fresh SR in `submitted` status.
func NewServiceRequest(
	ticketID, customerID uuid.UUID,
	requestType ServiceRequestType,
	submittedBy *uuid.UUID,
	payload map[string]any,
) (*ServiceRequest, error) {
	if ticketID == uuid.Nil {
		return nil, errors.Validation("cs.sr.ticket_required", "ticket_id is required")
	}
	if customerID == uuid.Nil {
		return nil, errors.Validation("cs.sr.customer_required", "customer_id is required")
	}
	if !requestType.Valid() {
		return nil, errors.Validation("cs.sr.type_invalid", "request_type is not recognized")
	}
	now := time.Now().UTC()
	initial := SRStatusSubmitted
	// Requests that do not require approval auto-flow to approved on
	// create — the usecase will then call StartFulfillment if a WO
	// bridge is wired.
	if !requestType.RequiresApproval() {
		initial = SRStatusApproved
	}
	return &ServiceRequest{
		ID:          uuid.New(),
		TicketID:    ticketID,
		CustomerID:  customerID,
		RequestType: requestType,
		Status:      initial,
		SubmittedBy: submittedBy,
		Payload:     payload,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Approve flips submitted → approved. Records who and when.
func (sr *ServiceRequest) Approve(by uuid.UUID) error {
	if sr.Status != SRStatusSubmitted {
		return errors.Conflict("cs.sr.cannot_approve",
			"service request must be submitted to approve, got "+string(sr.Status))
	}
	if by == uuid.Nil {
		return errors.Validation("cs.sr.approver_required", "approver user_id is required")
	}
	now := time.Now().UTC()
	sr.Status = SRStatusApproved
	sr.ApprovedBy = &by
	sr.ApprovalDecisionAt = &now
	sr.UpdatedAt = now
	return nil
}

// Reject flips submitted → rejected. Reason is required.
func (sr *ServiceRequest) Reject(by uuid.UUID, reason string) error {
	if sr.Status != SRStatusSubmitted {
		return errors.Conflict("cs.sr.cannot_reject",
			"service request must be submitted to reject, got "+string(sr.Status))
	}
	if by == uuid.Nil {
		return errors.Validation("cs.sr.rejector_required", "rejector user_id is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("cs.sr.reason_required", "rejection reason is required")
	}
	now := time.Now().UTC()
	sr.Status = SRStatusRejected
	sr.ApprovedBy = &by
	sr.ApprovalDecisionAt = &now
	sr.RejectionReason = reason
	sr.UpdatedAt = now
	return nil
}

// StartFulfillment flips approved → in_progress. Used after a WO has
// been spawned (or any other fulfilment artefact has been created).
func (sr *ServiceRequest) StartFulfillment() error {
	if sr.Status != SRStatusApproved {
		return errors.Conflict("cs.sr.cannot_start",
			"service request must be approved to start fulfilment, got "+string(sr.Status))
	}
	sr.Status = SRStatusInProgress
	sr.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkFulfilled flips in_progress → fulfilled. Stamps fulfilled_at.
func (sr *ServiceRequest) MarkFulfilled(at time.Time) error {
	if sr.Status != SRStatusInProgress {
		return errors.Conflict("cs.sr.cannot_fulfill",
			"service request must be in_progress to fulfil, got "+string(sr.Status))
	}
	v := at.UTC()
	sr.FulfilledAt = &v
	sr.Status = SRStatusFulfilled
	sr.UpdatedAt = v
	return nil
}

// Cancel flips approved or in_progress → cancelled. Submitted requests
// should be rejected, not cancelled.
func (sr *ServiceRequest) Cancel(by uuid.UUID, reason string) error {
	switch sr.Status {
	case SRStatusApproved, SRStatusInProgress:
		// ok
	default:
		return errors.Conflict("cs.sr.cannot_cancel",
			"service request cannot be cancelled in status "+string(sr.Status))
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "no reason given"
	}
	now := time.Now().UTC()
	sr.Status = SRStatusCancelled
	sr.CancelledReason = reason
	sr.UpdatedAt = now
	_ = by // by recorded via ticket_events at the usecase layer
	return nil
}

// IsTerminal reports whether the SR can no longer transition.
func (sr *ServiceRequest) IsTerminal() bool {
	return sr.Status == SRStatusFulfilled ||
		sr.Status == SRStatusRejected ||
		sr.Status == SRStatusCancelled
}
