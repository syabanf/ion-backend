package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// RefundStatus tracks the refund lifecycle.
//
// State machine:
//
//	requested → approved → processing → completed              (positive path)
//	         ↘ rejected                                         (terminal — admin denied)
//	         processing → failed                                (terminal — gateway error)
//
// Finance reviews requested refunds, approves them (recording who
// approved), then the gateway client submits the refund and waits
// for the gateway's response webhook to flip processing → completed.
type RefundStatus string

const (
	RefundStatusRequested  RefundStatus = "requested"
	RefundStatusApproved   RefundStatus = "approved"
	RefundStatusProcessing RefundStatus = "processing"
	RefundStatusCompleted  RefundStatus = "completed"
	RefundStatusRejected   RefundStatus = "rejected"
	RefundStatusFailed     RefundStatus = "failed"
)

// IsTerminal reports whether the refund is at end-of-line.
func (s RefundStatus) IsTerminal() bool {
	switch s {
	case RefundStatusCompleted, RefundStatusRejected, RefundStatusFailed:
		return true
	}
	return false
}

// Refund is a single refund attempt against a payment intent. Multiple
// refunds may exist per intent (e.g. partial refund #1 of 30k, then
// partial refund #2 of 20k). The intent's `RefundedAmount` is the
// cumulative authoritative total — the refund service recomputes it
// from completed refund rows on every transition.
type Refund struct {
	ID                 uuid.UUID
	PaymentIntentID    uuid.UUID
	Amount             float64
	Reason             string
	Status             RefundStatus
	ExternalRefundRef  *string
	RequestedBy        *uuid.UUID
	ApprovedBy         *uuid.UUID
	ApprovedAt         *time.Time
	CompletedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewRefund constructs a refund in the 'requested' state. The intent
// id + a positive amount are required; reason is optional but
// strongly encouraged so the approver has context.
func NewRefund(intentID uuid.UUID, amount float64, reason string, requestedBy *uuid.UUID) (*Refund, error) {
	if intentID == uuid.Nil {
		return nil, errors.Validation("refund.intent_required", "payment_intent_id is required")
	}
	if amount <= 0 {
		return nil, errors.Validation("refund.amount_invalid", "amount must be > 0")
	}
	now := time.Now().UTC()
	return &Refund{
		ID:              uuid.New(),
		PaymentIntentID: intentID,
		Amount:          amount,
		Reason:          strings.TrimSpace(reason),
		Status:          RefundStatusRequested,
		RequestedBy:     requestedBy,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// Approve flips requested → approved and records the approver. Only
// requested refunds can be approved; double-approve is a Conflict so
// audit trails stay clean.
func (r *Refund) Approve(by uuid.UUID) error {
	if r.Status != RefundStatusRequested {
		return errors.Conflict(
			"refund.cannot_approve",
			"only requested refunds can be approved",
		)
	}
	if by == uuid.Nil {
		return errors.Validation("refund.approver_required", "approver user id is required")
	}
	now := time.Now().UTC()
	r.ApprovedBy = &by
	r.ApprovedAt = &now
	r.Status = RefundStatusApproved
	r.UpdatedAt = now
	return nil
}

// Reject flips requested → rejected. Terminal.
func (r *Refund) Reject(reason string) error {
	if r.Status != RefundStatusRequested {
		return errors.Conflict(
			"refund.cannot_reject",
			"only requested refunds can be rejected",
		)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		// Preserve the original reason + reviewer rationale by appending.
		if r.Reason != "" {
			r.Reason = r.Reason + " | rejected: " + reason
		} else {
			r.Reason = "rejected: " + reason
		}
	}
	r.Status = RefundStatusRejected
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// StartProcessing flips approved → processing once the gateway client
// has been called. The gateway's eventual webhook flips this to
// completed (via MarkCompleted) or failed (via MarkFailed).
func (r *Refund) StartProcessing() error {
	if r.Status != RefundStatusApproved {
		return errors.Conflict(
			"refund.cannot_start_processing",
			"only approved refunds can move to processing",
		)
	}
	r.Status = RefundStatusProcessing
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkCompleted flips processing → completed and snapshots the
// gateway's external refund reference (for finance lookup).
func (r *Refund) MarkCompleted(externalRef string) error {
	if r.Status != RefundStatusProcessing {
		return errors.Conflict(
			"refund.cannot_complete",
			"only processing refunds can be marked completed",
		)
	}
	ref := strings.TrimSpace(externalRef)
	if ref != "" {
		r.ExternalRefundRef = &ref
	}
	now := time.Now().UTC()
	r.CompletedAt = &now
	r.Status = RefundStatusCompleted
	r.UpdatedAt = now
	return nil
}

// MarkFailed flips processing → failed and captures the gateway error.
// Terminal — finance must request a new refund if they want to retry.
func (r *Refund) MarkFailed(reason string) error {
	if r.Status != RefundStatusProcessing {
		return errors.Conflict(
			"refund.cannot_fail",
			"only processing refunds can be marked failed",
		)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		if r.Reason != "" {
			r.Reason = r.Reason + " | failed: " + reason
		} else {
			r.Reason = "failed: " + reason
		}
	}
	r.Status = RefundStatusFailed
	r.UpdatedAt = time.Now().UTC()
	return nil
}
