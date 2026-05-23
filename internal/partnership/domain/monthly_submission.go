package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SubmissionStatus is the monthly-submission lifecycle enum.
//
// State machine:
//
//	draft → submitted → confirmed     (settlement issued on confirm)
//	                ↘ returned → draft (re-submit via MarkDraft)
//	draft|returned → cancelled        (terminal)
//
// The DB enforces the enum via CHECK; the domain enforces legal
// transitions in the methods below.
type SubmissionStatus string

const (
	SubmissionStatusDraft     SubmissionStatus = "draft"
	SubmissionStatusSubmitted SubmissionStatus = "submitted"
	SubmissionStatusConfirmed SubmissionStatus = "confirmed"
	SubmissionStatusReturned  SubmissionStatus = "returned"
	SubmissionStatusCancelled SubmissionStatus = "cancelled"
)

// MonthlySubmission is one (reseller, year, month) row.
//
// One row per reseller-month (the UNIQUE constraint on
// (reseller_account_id, period_year, period_month) enforces this at
// the DB layer). A returned submission flips back to draft via
// MarkDraft() and is re-submitted in place — the row id never changes.
type MonthlySubmission struct {
	ID                uuid.UUID
	AgreementID       uuid.UUID
	ResellerAccountID uuid.UUID
	PeriodYear        int
	PeriodMonth       int
	Status            SubmissionStatus

	// Revenue + subscriber figures — nullable on draft, required on
	// submit. The Submit() method refuses to flip the status without
	// these populated.
	GrossRevenue    *float64
	NetRevenue      *float64
	SubscriberCount *int
	ChurnCount      *int

	// Evidence — URL + sha256 hash of the supporting document. The
	// Submit() method refuses without an evidence URL.
	EvidenceURL  string
	EvidenceHash string

	SubmittedBy    *uuid.UUID
	SubmittedAt    *time.Time
	ConfirmedBy    *uuid.UUID
	ConfirmedAt    *time.Time
	ReturnedReason string
	ReturnedAt     *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMonthlySubmission constructs a fresh draft row.
//
// Validation: year > 2000, month in 1..12. The caller (usecase) is
// responsible for the cross-row "no duplicate (reseller, year, month)"
// check — that's enforced at the DB level by the UNIQUE constraint
// but the usecase short-circuits with a Conflict error before INSERT
// for a nicer error message.
func NewMonthlySubmission(agreementID, resellerAccountID uuid.UUID, year, month int) (*MonthlySubmission, error) {
	if year < 2000 {
		return nil, errors.Validation("submission.year_invalid", "year is invalid")
	}
	if month < 1 || month > 12 {
		return nil, errors.Validation("submission.month_invalid", "month must be 1..12")
	}
	if agreementID == uuid.Nil {
		return nil, errors.Validation("submission.agreement_required", "agreement_id is required")
	}
	if resellerAccountID == uuid.Nil {
		return nil, errors.Validation("submission.reseller_required", "reseller_account_id is required")
	}
	now := time.Now().UTC()
	return &MonthlySubmission{
		ID:                uuid.New(),
		AgreementID:       agreementID,
		ResellerAccountID: resellerAccountID,
		PeriodYear:        year,
		PeriodMonth:       month,
		Status:            SubmissionStatusDraft,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// PeriodEnd returns the last day of the submission's period, in UTC.
// Used by the settlement issuer to look up the agreement that was
// active on the closing day of the reported month.
func (s *MonthlySubmission) PeriodEnd() time.Time {
	first := time.Date(s.PeriodYear, time.Month(s.PeriodMonth), 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, 1, -1)
}

// Submit flips draft|returned → submitted. Requires:
//   - GrossRevenue, NetRevenue, SubscriberCount populated
//   - EvidenceURL non-empty
//
// Re-submission after a 'returned' ruling is allowed — the reseller
// fixes the figures, calls MarkDraft() (which clears the returned
// reason), updates the data, then calls Submit() again. The row id
// stays the same throughout.
func (s *MonthlySubmission) Submit(by uuid.UUID) error {
	if s.Status != SubmissionStatusDraft && s.Status != SubmissionStatusReturned {
		return errors.Conflict(
			"submission.cannot_submit",
			"only draft or returned submissions can be submitted",
		)
	}
	if s.GrossRevenue == nil {
		return errors.Validation("submission.gross_revenue_required", "gross_revenue is required")
	}
	if s.NetRevenue == nil {
		return errors.Validation("submission.net_revenue_required", "net_revenue is required")
	}
	if s.SubscriberCount == nil {
		return errors.Validation("submission.subscriber_count_required", "subscriber_count is required")
	}
	if strings.TrimSpace(s.EvidenceURL) == "" {
		return errors.Validation("submission.evidence_required", "evidence_url is required")
	}
	if by == uuid.Nil {
		return errors.Validation("submission.submitted_by_required", "submitted_by is required")
	}
	now := time.Now().UTC()
	s.Status = SubmissionStatusSubmitted
	s.SubmittedBy = &by
	s.SubmittedAt = &now
	// Clear the returned-reason if this is a re-submit so the row
	// presents cleanly post-flip.
	s.ReturnedReason = ""
	s.ReturnedAt = nil
	s.UpdatedAt = now
	return nil
}

// Confirm flips submitted → confirmed. The usecase calls this and then
// immediately issues a settlement; the settlement issuance is the
// next step but lives in usecase/settlement.go.
func (s *MonthlySubmission) Confirm(by uuid.UUID) error {
	if s.Status != SubmissionStatusSubmitted {
		return errors.Conflict(
			"submission.cannot_confirm",
			"only submitted submissions can be confirmed",
		)
	}
	if by == uuid.Nil {
		return errors.Validation("submission.confirmed_by_required", "confirmed_by is required")
	}
	now := time.Now().UTC()
	s.Status = SubmissionStatusConfirmed
	s.ConfirmedBy = &by
	s.ConfirmedAt = &now
	s.UpdatedAt = now
	return nil
}

// Return flips submitted → returned with a Finance Review comment.
// Reason is mandatory so the reseller-portal can show "why was my
// submission sent back". (TC-PMS-005.)
func (s *MonthlySubmission) Return(reason string, at time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("submission.return_reason_required", "reason is required")
	}
	if s.Status != SubmissionStatusSubmitted {
		return errors.Conflict(
			"submission.cannot_return",
			"only submitted submissions can be returned",
		)
	}
	s.Status = SubmissionStatusReturned
	s.ReturnedReason = reason
	s.ReturnedAt = &at
	s.UpdatedAt = at
	return nil
}

// MarkDraft flips a returned submission back to draft so the reseller
// can re-edit it. Used by the usecase's PATCH path — touching a
// returned submission via PATCH implicitly re-drafts it.
func (s *MonthlySubmission) MarkDraft() error {
	if s.Status != SubmissionStatusReturned {
		return errors.Conflict(
			"submission.cannot_redraft",
			"only returned submissions can be re-drafted",
		)
	}
	s.Status = SubmissionStatusDraft
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// Cancel is the terminal exit from draft or returned. Confirmed
// submissions can't be cancelled — that would leave an orphan
// settlement; the operator must instead cancel the settlement first
// (which we don't auto-revert; it stays cancelled).
func (s *MonthlySubmission) Cancel() error {
	if s.Status == SubmissionStatusCancelled {
		return nil // idempotent
	}
	if s.Status != SubmissionStatusDraft && s.Status != SubmissionStatusReturned {
		return errors.Conflict(
			"submission.cannot_cancel",
			"only draft or returned submissions can be cancelled",
		)
	}
	s.Status = SubmissionStatusCancelled
	s.UpdatedAt = time.Now().UTC()
	return nil
}
