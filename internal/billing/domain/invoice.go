// Package domain holds the billing context's entities + invariants.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

type InvoiceType string

const (
	InvoiceTypeOTC          InvoiceType = "otc"
	InvoiceTypeRecurring    InvoiceType = "recurring"
	InvoiceTypeExcessCable  InvoiceType = "excess_cable"
	InvoiceTypeAddon        InvoiceType = "addon"
	InvoiceTypeMilestone    InvoiceType = "milestone"
)

// InvoiceStatus — lifecycle.
//
//	draft     — not yet sent; safe to edit lines.
//	issued    — delivered to the customer; awaiting payment.
//	paid      — fully paid (sum of confirmed payments ≥ total).
//	overdue   — past due_date without payment (round-1 not auto-set).
//	cancelled — voided; no further work.
type InvoiceStatus string

const (
	InvoiceStatusDraft     InvoiceStatus = "draft"
	InvoiceStatusIssued    InvoiceStatus = "issued"
	InvoiceStatusPaid      InvoiceStatus = "paid"
	InvoiceStatusOverdue   InvoiceStatus = "overdue"
	InvoiceStatusCancelled InvoiceStatus = "cancelled"
)

type Invoice struct {
	ID            uuid.UUID
	InvoiceNumber string
	CustomerID    uuid.UUID
	OrderID       *uuid.UUID
	InvoiceType   InvoiceType
	InvoiceDate   time.Time
	DueDate       time.Time
	Subtotal      float64
	PPNRate       float64
	PPNAmount     float64
	Total         float64
	Status        InvoiceStatus
	PaidAt        *time.Time
	Notes         string
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type LineItem struct {
	ID          uuid.UUID
	InvoiceID   uuid.UUID
	LineOrder   int
	Description string
	ItemType    string
	Quantity    float64
	UnitPrice   float64
	Amount      float64
}

// NewInvoice constructs an unsent (draft) invoice. The service then either
// commits it as 'draft' or calls Issue() before persisting depending on flow.
// PPN math is computed here so callers can't desync subtotal vs total.
func NewInvoice(customerID uuid.UUID, orderID *uuid.UUID, typ InvoiceType, lines []LineItem, ppnRate float64, dueDate time.Time, createdBy *uuid.UUID, notes string) (*Invoice, []LineItem, error) {
	if customerID == uuid.Nil {
		return nil, nil, errors.Validation("invoice.customer_required", "customer_id is required")
	}
	if len(lines) == 0 {
		return nil, nil, errors.Validation("invoice.empty", "invoice must have at least one line")
	}
	if ppnRate < 0 || ppnRate > 100 {
		return nil, nil, errors.Validation("invoice.ppn_invalid", "ppn_rate must be 0..100")
	}

	now := time.Now().UTC()
	if dueDate.IsZero() {
		dueDate = now.AddDate(0, 0, 7)
	}
	id := uuid.New()
	out := make([]LineItem, 0, len(lines))
	subtotal := 0.0
	for i, l := range lines {
		l.ID = uuid.New()
		l.InvoiceID = id
		if l.LineOrder == 0 {
			l.LineOrder = i + 1
		}
		if l.Quantity == 0 {
			l.Quantity = 1
		}
		if l.Amount == 0 {
			l.Amount = l.UnitPrice * l.Quantity
		}
		subtotal += l.Amount
		out = append(out, l)
	}

	ppn := round2(subtotal * ppnRate / 100)
	total := round2(subtotal + ppn)

	return &Invoice{
		ID:            id,
		InvoiceNumber: GenerateInvoiceNumber(now, typ),
		CustomerID:    customerID,
		OrderID:       orderID,
		InvoiceType:   typ,
		InvoiceDate:   now,
		DueDate:       dueDate,
		Subtotal:      round2(subtotal),
		PPNRate:       ppnRate,
		PPNAmount:     ppn,
		Total:         total,
		Status:        InvoiceStatusDraft,
		Notes:         strings.TrimSpace(notes),
		CreatedBy:     createdBy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, out, nil
}

// Issue moves draft → issued. Idempotent on a re-call (already issued is a
// conflict, not a silent no-op, so the UI surfaces the duplicate intent).
func (i *Invoice) Issue() error {
	if i.Status == InvoiceStatusIssued {
		return errors.Conflict("invoice.already_issued", "invoice already issued")
	}
	if i.Status != InvoiceStatusDraft {
		return errors.Conflict("invoice.bad_state", "only draft invoices can be issued")
	}
	i.Status = InvoiceStatusIssued
	i.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkPaid transitions to paid. Caller is responsible for ensuring payment
// total ≥ invoice total — we keep this invariant out of the domain since
// it requires aggregating payment rows; the service does that math.
func (i *Invoice) MarkPaid(at time.Time) error {
	if i.Status == InvoiceStatusPaid {
		return errors.Conflict("invoice.already_paid", "invoice already paid")
	}
	if i.Status == InvoiceStatusCancelled {
		return errors.Conflict("invoice.cancelled", "cannot pay a cancelled invoice")
	}
	i.Status = InvoiceStatusPaid
	t := at.UTC()
	i.PaidAt = &t
	i.UpdatedAt = time.Now().UTC()
	return nil
}

func (i *Invoice) Cancel() error {
	if i.Status == InvoiceStatusPaid {
		return errors.Conflict("invoice.paid_uncancellable", "cannot cancel a paid invoice")
	}
	if i.Status == InvoiceStatusCancelled {
		return errors.Conflict("invoice.already_cancelled", "invoice already cancelled")
	}
	i.Status = InvoiceStatusCancelled
	i.UpdatedAt = time.Now().UTC()
	return nil
}

func GenerateInvoiceNumber(t time.Time, typ InvoiceType) string {
	prefix := "INV"
	switch typ {
	case InvoiceTypeOTC:
		prefix = "OTC"
	case InvoiceTypeRecurring:
		prefix = "INV"
	case InvoiceTypeExcessCable:
		prefix = "EXC"
	case InvoiceTypeAddon:
		prefix = "ADD"
	case InvoiceTypeMilestone:
		prefix = "MIL"
	}
	return prefix + "-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

func round2(f float64) float64 {
	// Round half-up to 2 decimals to match Postgres NUMERIC(15,2).
	if f >= 0 {
		return float64(int64(f*100+0.5)) / 100
	}
	return float64(int64(f*100-0.5)) / 100
}
