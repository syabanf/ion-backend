package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// InvoiceStatus tracks the per-invoice lifecycle.
//
// Lifecycle:
//
//	open → paid                       (terminal via MarkPaid)
//	open → overdue → paid             (cron flips via MarkOverdue)
//	open|overdue → cancelled          (terminal via Cancel)
//
// The DB enforces the enum via CHECK; the domain enforces transitions.
// Once paid or cancelled, the row is read-only.
type InvoiceStatus string

const (
	InvoiceStatusOpen      InvoiceStatus = "open"
	InvoiceStatusPaid      InvoiceStatus = "paid"
	InvoiceStatusOverdue   InvoiceStatus = "overdue"
	InvoiceStatusCancelled InvoiceStatus = "cancelled"
)

// SubscriberInvoice is one issued invoice in the reseller inbox. The
// reseller tenant scope is the authoritative key; cross-tenant reads
// MUST be refused at the repo boundary.
//
// period_year + period_month track which calendar period the invoice
// covers; the invoice may be issued days/weeks after period end so
// IssuedAt is independent. DueAt drives the overdue evaluator —
// MarkOverdue() is called by the dashboard helper at month-end (or
// on read when the dashboard computes derived counts).
type SubscriberInvoice struct {
	ID                uuid.UUID
	ResellerAccountID uuid.UUID
	SubscriberID      uuid.UUID
	InvoiceNo         string
	PeriodYear        int
	PeriodMonth       int
	Amount            float64
	Status            InvoiceStatus
	IssuedAt          time.Time
	DueAt             *time.Time
	PaidAt            *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewSubscriberInvoice constructs an open invoice. Caller is
// responsible for ensuring the subscriber belongs to the same tenant
// (the usecase enforces this via a tenant-scoped FindForReseller).
func NewSubscriberInvoice(resellerID, subscriberID uuid.UUID, invoiceNo string, periodYear, periodMonth int, amount float64, dueAt *time.Time) (*SubscriberInvoice, error) {
	if resellerID == uuid.Nil {
		return nil, errors.Validation("invoice.reseller_required", "reseller_account_id is required")
	}
	if subscriberID == uuid.Nil {
		return nil, errors.Validation("invoice.subscriber_required", "subscriber_id is required")
	}
	invoiceNo = strings.TrimSpace(invoiceNo)
	if invoiceNo == "" {
		return nil, errors.Validation("invoice.no_required", "invoice_no is required")
	}
	if periodMonth != 0 && (periodMonth < 1 || periodMonth > 12) {
		return nil, errors.Validation("invoice.period_invalid", "period_month must be 1..12")
	}
	if amount < 0 {
		return nil, errors.Validation("invoice.amount_negative", "amount must be >= 0")
	}
	now := time.Now().UTC()
	return &SubscriberInvoice{
		ID:                uuid.New(),
		ResellerAccountID: resellerID,
		SubscriberID:      subscriberID,
		InvoiceNo:         invoiceNo,
		PeriodYear:        periodYear,
		PeriodMonth:       periodMonth,
		Amount:            amount,
		Status:            InvoiceStatusOpen,
		IssuedAt:          now,
		DueAt:             dueAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// MarkPaid moves open|overdue → paid. Idempotent on already-paid;
// refuses cancelled (can't pay a cancelled invoice).
func (i *SubscriberInvoice) MarkPaid(at time.Time) error {
	switch i.Status {
	case InvoiceStatusPaid:
		return nil
	case InvoiceStatusCancelled:
		return errors.Conflict("invoice.cannot_pay", "cancelled invoices cannot be paid")
	}
	atUTC := at.UTC()
	i.Status = InvoiceStatusPaid
	i.PaidAt = &atUTC
	i.UpdatedAt = atUTC
	return nil
}

// MarkOverdue moves open → overdue when DueAt is past. Called by the
// dashboard helper / a future cron. Idempotent on already-overdue;
// no-op on paid/cancelled (terminal). Caller passes the "as of" time
// so the same clock is used across an evaluation batch.
func (i *SubscriberInvoice) MarkOverdue(asOf time.Time) error {
	if i.Status != InvoiceStatusOpen {
		return nil
	}
	if i.DueAt == nil {
		return nil
	}
	if !asOf.After(*i.DueAt) {
		return nil
	}
	i.Status = InvoiceStatusOverdue
	i.UpdatedAt = asOf.UTC()
	return nil
}

// Cancel moves open|overdue → cancelled. Terminal. Refuses paid (we
// don't retroactively cancel a paid invoice — that's a separate
// reversal flow not in scope for this wave).
func (i *SubscriberInvoice) Cancel() error {
	switch i.Status {
	case InvoiceStatusCancelled:
		return nil
	case InvoiceStatusPaid:
		return errors.Conflict("invoice.cannot_cancel", "paid invoices cannot be cancelled")
	}
	i.Status = InvoiceStatusCancelled
	i.UpdatedAt = time.Now().UTC()
	return nil
}
