package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Negotiation — top-level lifecycle row, one per BOQ
// =====================================================================

type NegotiationStatus string

const (
	NegotiationStatusInactive  NegotiationStatus = "inactive"
	NegotiationStatusActive    NegotiationStatus = "active"
	NegotiationStatusCompleted NegotiationStatus = "completed"
	NegotiationStatusAborted   NegotiationStatus = "aborted"
)

type Negotiation struct {
	ID                  uuid.UUID
	BOQVersionID        uuid.UUID
	Status              NegotiationStatus
	ActivatedAt         *time.Time
	ActivatedBy         *uuid.UUID
	CompletedAt         *time.Time
	AbortedAt           *time.Time
	AbortReason         string
	// Stamped when the completion hook fires the re-quote — links the
	// negotiation outcome to the artifact it produced.
	ResultingQuotationID *uuid.UUID
	Revision            int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// NewNegotiation seeds an inactive row attached to a BOQ. The usecase
// creates this when the BOQ is approved (so it's ready to activate
// once a quotation gets rejected). One per BOQ — unique constraint
// at the DB level enforces it.
func NewNegotiation(boqVersionID uuid.UUID) (*Negotiation, error) {
	if boqVersionID == uuid.Nil {
		return nil, errors.Validation("negotiation.boq_required", "boq_version_id is required")
	}
	now := time.Now().UTC()
	return &Negotiation{
		ID:           uuid.New(),
		BOQVersionID: boqVersionID,
		Status:       NegotiationStatusInactive,
		Revision:     1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Activate flips inactive → active. Per TC-NEG-003 the usecase
// must verify a quotation has been issued for this BOQ before
// calling — the domain only enforces the state transition.
func (n *Negotiation) Activate(actor uuid.UUID) error {
	if n.Status != NegotiationStatusInactive {
		return errors.Conflict(
			"negotiation.invalid_state_transition",
			"can only activate from inactive (current: "+string(n.Status)+")",
		)
	}
	now := time.Now().UTC()
	n.Status = NegotiationStatusActive
	n.ActivatedAt = &now
	n.ActivatedBy = &actor
	n.UpdatedAt = now
	return nil
}

// MarkCompleted is called when a round's approval chain finishes.
// Caller is responsible for setting ResultingQuotationID after the
// re-quote hook fires.
func (n *Negotiation) MarkCompleted(quotationID *uuid.UUID) error {
	if n.Status != NegotiationStatusActive {
		return errors.Conflict(
			"negotiation.invalid_state_transition",
			"can only complete from active",
		)
	}
	now := time.Now().UTC()
	n.Status = NegotiationStatusCompleted
	n.CompletedAt = &now
	n.ResultingQuotationID = quotationID
	n.UpdatedAt = now
	return nil
}

// Abort handles Edge #1 (BOQ revision started mid-flight) + manual
// abort. Reason is required so the audit trail explains why.
func (n *Negotiation) Abort(reason string) error {
	if n.Status != NegotiationStatusActive {
		return errors.Conflict(
			"negotiation.invalid_state_transition",
			"can only abort from active",
		)
	}
	if reason == "" {
		return errors.Validation("negotiation.abort_reason_required", "reason is required")
	}
	now := time.Now().UTC()
	n.Status = NegotiationStatusAborted
	n.AbortedAt = &now
	n.AbortReason = reason
	n.UpdatedAt = now
	return nil
}
