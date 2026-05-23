package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// CreditNoteStatus — lifecycle.
//
//	draft   — staged; reversible; not yet effective.
//	issued  — approved + assigned credit_no; visible to customer.
//	applied — folded against a payment / invoice balance; terminal.
//	voided  — cancelled before or after issue; terminal.
type CreditNoteStatus string

const (
	CreditNoteStatusDraft   CreditNoteStatus = "draft"
	CreditNoteStatusIssued  CreditNoteStatus = "issued"
	CreditNoteStatusApplied CreditNoteStatus = "applied"
	CreditNoteStatusVoided  CreditNoteStatus = "voided"
)

// CreditNote — parent-invoice credit document.
//
// State machine (TC-IGE-008):
//
//	  ┌─────────┐  Issue   ┌────────┐  Apply  ┌─────────┐
//	  │  draft  │──────────│ issued │─────────│ applied │
//	  └─────────┘          └────────┘         └─────────┘
//	       │ Void              │ Void
//	       ▼                   ▼
//	  ┌─────────┐          ┌─────────┐
//	  │ voided  │          │ voided  │
//	  └─────────┘          └─────────┘
//
// 'applied' and 'voided' are terminal. The cn_number is assigned at Issue.
type CreditNote struct {
	ID         uuid.UUID
	InvoiceID  uuid.UUID
	CustomerID *uuid.UUID
	CreditNo   string
	Amount     float64
	Reason     string
	Status     CreditNoteStatus
	IssuedAt   *time.Time
	AppliedAt  *time.Time
	VoidedAt   *time.Time
	CreatedBy  *uuid.UUID
	ApprovedBy *uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewCreditNote constructs a draft CN. Amount must be >= 0 (we allow
// zero-amount credits for symbolic "this invoice was reversed" notes).
func NewCreditNote(invoiceID uuid.UUID, customerID *uuid.UUID, amount float64, reason string, createdBy *uuid.UUID) (*CreditNote, error) {
	if invoiceID == uuid.Nil {
		return nil, errors.Validation("credit_note.invoice_required", "invoice_id is required")
	}
	if amount < 0 {
		return nil, errors.Validation("credit_note.amount_invalid", "amount must be >= 0")
	}
	now := time.Now().UTC()
	return &CreditNote{
		ID:         uuid.New(),
		InvoiceID:  invoiceID,
		CustomerID: customerID,
		Amount:     amount,
		Reason:     strings.TrimSpace(reason),
		Status:     CreditNoteStatusDraft,
		CreatedBy:  createdBy,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Issue moves draft → issued and binds the credit_no. The caller picks
// the format (we don't generate here so the DB can hand out a counter).
func (c *CreditNote) Issue(creditNo string, approvedBy *uuid.UUID) error {
	if c.Status != CreditNoteStatusDraft {
		return errors.Conflict("credit_note.bad_state", "only draft credit notes can be issued")
	}
	creditNo = strings.TrimSpace(creditNo)
	if creditNo == "" {
		return errors.Validation("credit_note.credit_no_required", "credit_no is required to issue")
	}
	now := time.Now().UTC()
	c.CreditNo = creditNo
	c.Status = CreditNoteStatusIssued
	c.IssuedAt = &now
	c.ApprovedBy = approvedBy
	c.UpdatedAt = now
	return nil
}

// Apply transitions issued → applied. Idempotent re-apply is a conflict
// so the caller knows the second-apply intent was wrong.
func (c *CreditNote) Apply(at time.Time) error {
	if c.Status == CreditNoteStatusApplied {
		return errors.Conflict("credit_note.already_applied", "credit note already applied")
	}
	if c.Status != CreditNoteStatusIssued {
		return errors.Conflict("credit_note.bad_state", "only issued credit notes can be applied")
	}
	t := at.UTC()
	if t.IsZero() {
		t = time.Now().UTC()
	}
	c.Status = CreditNoteStatusApplied
	c.AppliedAt = &t
	c.UpdatedAt = time.Now().UTC()
	return nil
}

// Void cancels the CN (allowed from draft OR issued; not from applied).
// reason is required so the audit trail has a why.
func (c *CreditNote) Void(by *uuid.UUID, reason string) error {
	if c.Status == CreditNoteStatusApplied {
		return errors.Conflict("credit_note.applied_unvoidable", "cannot void an applied credit note")
	}
	if c.Status == CreditNoteStatusVoided {
		return errors.Conflict("credit_note.already_voided", "credit note already voided")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("credit_note.void_reason_required", "void reason is required")
	}
	now := time.Now().UTC()
	c.Status = CreditNoteStatusVoided
	c.VoidedAt = &now
	c.Reason = reason
	c.UpdatedAt = now
	_ = by // kept on the signature for symmetry with Issue/Apply audit;
	// the actual approval is captured at the repo level via ctx claims.
	return nil
}

// IsTerminal reports whether the CN is in a terminal state.
func (c *CreditNote) IsTerminal() bool {
	return c.Status == CreditNoteStatusApplied || c.Status == CreditNoteStatusVoided
}
