package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// QuotationStatus tracks the customer-facing artifact lifecycle.
//
//	issued     — live, valid_until in the future
//	expired    — valid_until passed; need a re-quote
//	superseded — v(N+1) issued; this row is read-only history
//	accepted   — customer signed; opportunity advances
//	rejected   — customer declined (rare — usually leads to negotiation)
//	cancelled  — voided by ops before delivery
type QuotationStatus string

const (
	QuotationStatusIssued     QuotationStatus = "issued"
	QuotationStatusExpired    QuotationStatus = "expired"
	QuotationStatusSuperseded QuotationStatus = "superseded"
	QuotationStatusAccepted   QuotationStatus = "accepted"
	QuotationStatusRejected   QuotationStatus = "rejected"
	QuotationStatusCancelled  QuotationStatus = "cancelled"
)

// Quotation is the customer-facing PDF artifact generated when a BOQ
// is approved (and re-generated when a Phase-4b negotiation completes
// against the same BOQ).
//
// `PDFBytes` lives on the domain struct so the usecase can save it
// transactionally with the row. The HTTP layer streams it on demand
// via a dedicated `/pdf` endpoint — the JSON-facing DTOs never
// embed it.
type Quotation struct {
	ID               uuid.UUID
	QuotationNumber  string
	VersionNo        int
	BOQVersionID     uuid.UUID
	OpportunityID    uuid.UUID
	Status           QuotationStatus
	SellTotal        float64
	CostTotal        float64
	MarginPct        float64
	Currency         string
	PDFBytes         []byte // raw PDF; transactional storage in DB at MVP
	PDFHash          string // SHA-256 hex of PDFBytes
	PDFBytesSize     int
	ValidFrom        time.Time
	ValidUntil       time.Time
	IssuedAt         time.Time
	AcceptedAt       *time.Time
	RejectedAt       *time.Time
	CancelledAt      *time.Time
	SupersededAt     *time.Time
	Notes            string
	Revision         int
	IssuedBy         *uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// Wave 101 — tax snapshot inherited from the source BOQ at issuance.
	// The usecase copies BOQ.TaxSnapshotHash into this field; downstream
	// invoice + faktur generation verifies the chain matches.
	TaxSnapshotHash *string
}

// InheritTaxSnapshot copies the BOQ's frozen tax snapshot hash onto the
// quotation. Returns a Conflict if the quotation already carries a
// hash that differs — once a quotation row exists, its snapshot is
// immutable.
func (q *Quotation) InheritTaxSnapshot(hash string) error {
	if hash == "" {
		return nil
	}
	if q.TaxSnapshotHash != nil && *q.TaxSnapshotHash != "" && *q.TaxSnapshotHash != hash {
		return errors.Conflict(
			"tax_snapshot.mismatch",
			"quotation tax_snapshot_hash already set to a different value — possible BOQ revision drift",
		)
	}
	h := hash
	q.TaxSnapshotHash = &h
	return nil
}

// NewQuotation constructs an issued v1 quotation. Caller (usecase)
// sets the PDF bytes + hash after rendering; we validate them in
// EnsurePDFReady before persistence.
func NewQuotation(boqVersionID, opportunityID uuid.UUID) (*Quotation, error) {
	if boqVersionID == uuid.Nil {
		return nil, errors.Validation("quotation.boq_required", "boq_version_id is required")
	}
	if opportunityID == uuid.Nil {
		return nil, errors.Validation("quotation.opportunity_required", "opportunity_id is required")
	}
	now := time.Now().UTC()
	// Default validity window: 30 days. Configurable per quote when
	// the Admin module lands; for MVP this matches PRD §6.4 commercial
	// guidance (most enterprise quotes are valid for a month).
	return &Quotation{
		ID:            uuid.New(),
		BOQVersionID:  boqVersionID,
		OpportunityID: opportunityID,
		VersionNo:     1,
		Status:        QuotationStatusIssued,
		Currency:      "IDR",
		ValidFrom:     now,
		ValidUntil:    now.Add(30 * 24 * time.Hour),
		IssuedAt:      now,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// GenerateQuotationNumber matches the BOQ/opportunity numbering convention.
func GenerateQuotationNumber(t time.Time) string {
	return "QT-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// EnsurePDFReady asserts the artifact bytes + hash + size are present
// before the usecase persists. Helps catch the "we forgot to render
// the PDF" bug at write time rather than at download time.
func (q *Quotation) EnsurePDFReady() error {
	if len(q.PDFBytes) == 0 {
		return errors.Validation("quotation.pdf_empty", "pdf_bytes must be present")
	}
	if q.PDFHash == "" {
		return errors.Validation("quotation.pdf_hash_missing", "pdf_hash must be computed before persist")
	}
	if q.PDFBytesSize <= 0 {
		q.PDFBytesSize = len(q.PDFBytes)
	}
	return nil
}

// MarkAccepted is called when the operator records customer sign-off.
// One-way: accepted quotes can't be reopened (the deal moves to the
// EWO / project execution phase). Cancel + re-issue if needed.
func (q *Quotation) MarkAccepted() error {
	if q.Status != QuotationStatusIssued {
		return errors.Conflict(
			"quotation.cannot_accept",
			"only issued quotations can be accepted",
		)
	}
	now := time.Now().UTC()
	q.Status = QuotationStatusAccepted
	q.AcceptedAt = &now
	q.UpdatedAt = now
	return nil
}

// MarkRejected is the explicit customer-declined path. Sales typically
// follows this with a Phase 4b negotiation; the rejected row stays
// put for audit + the new round produces a fresh v(N+1).
func (q *Quotation) MarkRejected(reason string) error {
	if q.Status != QuotationStatusIssued {
		return errors.Conflict(
			"quotation.cannot_reject",
			"only issued quotations can be rejected",
		)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("quotation.reject_reason_required", "rejection reason is required")
	}
	now := time.Now().UTC()
	q.Status = QuotationStatusRejected
	q.RejectedAt = &now
	q.Notes = appendNote(q.Notes, "REJECTED: "+reason)
	q.UpdatedAt = now
	return nil
}

// Cancel voids an issued quotation before delivery. Operators use
// this when a quote was generated in error (wrong BOQ pin, premature
// generation). Use sparingly — the audit trail makes "we sent then
// cancelled" visible.
func (q *Quotation) Cancel(reason string) error {
	if q.Status != QuotationStatusIssued {
		return errors.Conflict(
			"quotation.cannot_cancel",
			"only issued quotations can be cancelled",
		)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("quotation.cancel_reason_required", "cancellation reason is required")
	}
	now := time.Now().UTC()
	q.Status = QuotationStatusCancelled
	q.CancelledAt = &now
	q.Notes = appendNote(q.Notes, "CANCELLED: "+reason)
	q.UpdatedAt = now
	return nil
}

// Supersede is called by the usecase when v(N+1) of the same
// quotation_number gets issued (after a Phase 4b negotiation).
func (q *Quotation) Supersede() error {
	if q.Status != QuotationStatusIssued && q.Status != QuotationStatusRejected {
		return errors.Conflict(
			"quotation.cannot_supersede",
			"only issued/rejected quotations can be superseded",
		)
	}
	now := time.Now().UTC()
	q.Status = QuotationStatusSuperseded
	q.SupersededAt = &now
	q.UpdatedAt = now
	return nil
}

// IsExpired is a derived check (not a stored state until a sweeper
// flips status='expired' on a schedule). The current API surfaces
// it via a computed flag on the DTO.
func (q *Quotation) IsExpired(now time.Time) bool {
	return q.Status == QuotationStatusIssued && now.After(q.ValidUntil)
}

func appendNote(existing, note string) string {
	existing = strings.TrimSpace(existing)
	note = strings.TrimSpace(note)
	if existing == "" {
		return note
	}
	return existing + "\n" + note
}
