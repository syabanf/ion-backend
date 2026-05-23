package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SubmissionStatus is the lifecycle of a vendor cost-input submission.
//
// Lifecycle:
//
//	submitted → accepted (terminal positive; reviewer)
//	submitted → rejected (terminal negative; reviewer, reason required)
//	submitted → withdrawn (terminal admin; submitter only)
//
// Once terminal, the row is immutable — a follow-up cost requires a
// brand-new submission. This keeps the audit trail clean for the
// "vendor changed their quote" reporting surface.
type SubmissionStatus string

const (
	SubmissionStatusSubmitted SubmissionStatus = "submitted"
	SubmissionStatusAccepted  SubmissionStatus = "accepted"
	SubmissionStatusRejected  SubmissionStatus = "rejected"
	SubmissionStatusWithdrawn SubmissionStatus = "withdrawn"
)

// InputSubmission is one vendor cost-input attempt for one opportunity
// (optionally narrowed to one BOQ line). UnitCost is nullable so a
// submitter can flag "we can't bid this" with notes only — the BOQ
// flow treats those as informational, not as cost data.
type InputSubmission struct {
	ID              uuid.UUID
	OpportunityID   uuid.UUID
	ProviderID      uuid.UUID
	BOQLineID       *uuid.UUID
	UnitCost        *float64
	Notes           string
	Status          SubmissionStatus
	SubmittedBy     *uuid.UUID
	SubmittedAt     time.Time
	ReviewedBy      *uuid.UUID
	ReviewedAt      *time.Time
	RejectionReason string
}

// NewInputSubmission constructs a fresh submitted row. UnitCost is
// optional (some submissions are "we decline to bid"); when supplied
// it must be > 0 to dodge the zero-as-missing ambiguity.
func NewInputSubmission(
	opportunityID, providerID uuid.UUID,
	boqLineID *uuid.UUID,
	unitCost *float64,
	notes string,
	submittedBy *uuid.UUID,
) (*InputSubmission, error) {
	if opportunityID == uuid.Nil {
		return nil, errors.Validation("submission.opportunity_required", "opportunity_id is required")
	}
	if providerID == uuid.Nil {
		return nil, errors.Validation("submission.provider_required", "provider_id is required")
	}
	if unitCost != nil && *unitCost <= 0 {
		return nil, errors.Validation("submission.unit_cost_invalid", "unit_cost must be > 0 when supplied")
	}
	return &InputSubmission{
		ID:            uuid.New(),
		OpportunityID: opportunityID,
		ProviderID:    providerID,
		BOQLineID:     boqLineID,
		UnitCost:      unitCost,
		Notes:         strings.TrimSpace(notes),
		Status:        SubmissionStatusSubmitted,
		SubmittedBy:   submittedBy,
		SubmittedAt:   time.Now().UTC(),
	}, nil
}

// Accept moves submitted → accepted. Records the reviewer. Idempotent
// on already-accepted; rejects every other source state so a
// previously-rejected row can't be quietly resurrected.
func (s *InputSubmission) Accept(reviewer uuid.UUID) error {
	if s.Status == SubmissionStatusAccepted {
		return nil
	}
	if s.Status != SubmissionStatusSubmitted {
		return errors.Conflict(
			"submission.cannot_accept",
			"only submitted submissions can be accepted",
		)
	}
	now := time.Now().UTC()
	s.Status = SubmissionStatusAccepted
	s.ReviewedBy = &reviewer
	s.ReviewedAt = &now
	return nil
}

// Reject moves submitted → rejected. Reason is required so the
// submitter sees a human-readable rejection in their inbox.
func (s *InputSubmission) Reject(reviewer uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("submission.reject_reason_required", "reason is required")
	}
	if s.Status != SubmissionStatusSubmitted {
		return errors.Conflict(
			"submission.cannot_reject",
			"only submitted submissions can be rejected",
		)
	}
	now := time.Now().UTC()
	s.Status = SubmissionStatusRejected
	s.ReviewedBy = &reviewer
	s.ReviewedAt = &now
	s.RejectionReason = reason
	return nil
}

// Withdraw moves submitted → withdrawn. Only the submitter can withdraw
// (the usecase enforces that — the domain just enforces the source
// state). Once withdrawn, the submitter must file a fresh row to
// re-bid.
func (s *InputSubmission) Withdraw() error {
	if s.Status == SubmissionStatusWithdrawn {
		return nil
	}
	if s.Status != SubmissionStatusSubmitted {
		return errors.Conflict(
			"submission.cannot_withdraw",
			"only submitted submissions can be withdrawn",
		)
	}
	s.Status = SubmissionStatusWithdrawn
	return nil
}

// IsTerminal reports whether the submission has reached a final state.
// Terminal rows are immutable + invisible to "open review queue" reads.
func (s *InputSubmission) IsTerminal() bool {
	switch s.Status {
	case SubmissionStatusAccepted, SubmissionStatusRejected, SubmissionStatusWithdrawn:
		return true
	}
	return false
}
