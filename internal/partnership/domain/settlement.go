package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SettlementStatus is the settlement-lifecycle enum.
//
// State machine:
//
//	pending → approved → paid (terminal)
//	       ↘ cancelled        (also from approved)
type SettlementStatus string

const (
	SettlementStatusPending   SettlementStatus = "pending"
	SettlementStatusApproved  SettlementStatus = "approved"
	SettlementStatusPaid      SettlementStatus = "paid"
	SettlementStatusCancelled SettlementStatus = "cancelled"
)

// Settlement is the per-confirmed-submission revenue-share invoice.
//
// Created in pending state by the usecase's IssueSettlementForSubmission
// hook when a monthly submission is confirmed. Finance approves
// (→approved), then marks paid (→paid). Cancellation is allowed from
// pending or approved.
//
// AgreementTermsSnapshot freezes the agreement payload at confirm time
// so later edits to partnership.agreements can never retroactively
// rewrite a closed settlement (TC-PS-005).
//
// FormulaHash is sha256 of the canonical formula inputs — if any of
// gross/net/revshare/tax/payable/agreement_id/period are mutated
// downstream the hash comparison flags the row as tampered.
type Settlement struct {
	ID                       uuid.UUID
	SubmissionID             uuid.UUID
	AgreementID              uuid.UUID
	AgreementTermsSnapshot   map[string]any
	GrossRevenue             float64
	NetRevenue               float64
	RevshareAmount           float64
	TaxAmount                float64
	PayableAmount            float64
	FormulaHash              string
	Status                   SettlementStatus
	PDFURL                   string
	PDFHash                  string
	ApprovedBy               *uuid.UUID
	ApprovedAt               *time.Time
	PaidAt                   *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time

	// PeriodYear / PeriodMonth — copied from the submission at issue
	// time and folded into the formula hash so a settlement can't be
	// silently re-pointed to a different period.
	PeriodYear  int
	PeriodMonth int
}

// NewSettlement constructs a fresh pending settlement.
//
// The caller (usecase) is responsible for:
//   - looking up the agreement active at submission.PeriodEnd
//   - snapshotting agreement.TermsSnapshot()
//   - computing gross/net/revshare/tax/payable per the agreement
//   - calling ComputeFormulaHash() to populate FormulaHash
//
// We don't take a constructor with every field because the formula
// math is the usecase's responsibility, not the domain's; the domain
// just holds the row and enforces the state machine.
func NewSettlement(
	submissionID, agreementID uuid.UUID,
	periodYear, periodMonth int,
	gross, net, revshare, tax, payable float64,
	termsSnapshot map[string]any,
) (*Settlement, error) {
	if submissionID == uuid.Nil {
		return nil, errors.Validation("settlement.submission_required", "submission_id is required")
	}
	if agreementID == uuid.Nil {
		return nil, errors.Validation("settlement.agreement_required", "agreement_id is required")
	}
	if periodMonth < 1 || periodMonth > 12 {
		return nil, errors.Validation("settlement.period_month_invalid", "period_month must be 1..12")
	}
	if termsSnapshot == nil {
		termsSnapshot = map[string]any{}
	}
	now := time.Now().UTC()
	s := &Settlement{
		ID:                     uuid.New(),
		SubmissionID:           submissionID,
		AgreementID:            agreementID,
		AgreementTermsSnapshot: termsSnapshot,
		GrossRevenue:           gross,
		NetRevenue:             net,
		RevshareAmount:         revshare,
		TaxAmount:              tax,
		PayableAmount:          payable,
		Status:                 SettlementStatusPending,
		PeriodYear:             periodYear,
		PeriodMonth:            periodMonth,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	s.FormulaHash = s.ComputeFormulaHash()
	return s, nil
}

// ComputeFormulaHash returns sha256(canonical-formula-inputs) as hex.
//
// Format:
//
//	gross=<g>|net=<n>|revshare=<r>|tax=<t>|payable=<p>|agreement=<id>|period=<yyyy-mm>
//
// Numbers are formatted with %.2f so the same logical amount produces
// the same hash regardless of float printf vagaries. The agreement id
// + period make the hash specific to this settlement row so a copy-
// paste across periods is detectable.
func (s *Settlement) ComputeFormulaHash() string {
	canonical := fmt.Sprintf(
		"gross=%.2f|net=%.2f|revshare=%.2f|tax=%.2f|payable=%.2f|agreement=%s|period=%04d-%02d",
		s.GrossRevenue,
		s.NetRevenue,
		s.RevshareAmount,
		s.TaxAmount,
		s.PayableAmount,
		s.AgreementID.String(),
		s.PeriodYear,
		s.PeriodMonth,
	)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyFormulaHash recomputes the canonical hash and compares it to
// the stored value. Returns true on match, false otherwise. Used by
// the settlement-list endpoint to surface a "tampered" flag without
// blocking the read.
func (s *Settlement) VerifyFormulaHash() bool {
	return strings.EqualFold(s.FormulaHash, s.ComputeFormulaHash())
}

// Approve flips pending → approved.
func (s *Settlement) Approve(by uuid.UUID) error {
	if s.Status == SettlementStatusApproved {
		return nil // idempotent
	}
	if s.Status != SettlementStatusPending {
		return errors.Conflict(
			"settlement.cannot_approve",
			"only pending settlements can be approved",
		)
	}
	if by == uuid.Nil {
		return errors.Validation("settlement.approved_by_required", "approved_by is required")
	}
	now := time.Now().UTC()
	s.Status = SettlementStatusApproved
	s.ApprovedBy = &by
	s.ApprovedAt = &now
	s.UpdatedAt = now
	return nil
}

// Pay flips approved → paid.
//
// `at` is the payment timestamp (typically pulled from the payment-
// proof upload); we don't default to NOW() so the caller can backdate
// for bank-transfer settlements that arrived earlier.
func (s *Settlement) Pay(at time.Time) error {
	if s.Status == SettlementStatusPaid {
		return nil // idempotent
	}
	if s.Status != SettlementStatusApproved {
		return errors.Conflict(
			"settlement.cannot_pay",
			"only approved settlements can be marked paid",
		)
	}
	s.Status = SettlementStatusPaid
	s.PaidAt = &at
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// Cancel is the terminal exit from pending or approved. Paid
// settlements can't be cancelled (the payment already happened) —
// the operator must issue a reversing transaction outside this surface.
func (s *Settlement) Cancel() error {
	if s.Status == SettlementStatusCancelled {
		return nil // idempotent
	}
	if s.Status == SettlementStatusPaid {
		return errors.Conflict(
			"settlement.cannot_cancel_paid",
			"paid settlements cannot be cancelled",
		)
	}
	s.Status = SettlementStatusCancelled
	s.UpdatedAt = time.Now().UTC()
	return nil
}
