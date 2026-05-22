package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// InvoicePlanStatus — draft (building), active (issuing), cancelled.
type InvoicePlanStatus string

const (
	InvoicePlanStatusDraft     InvoicePlanStatus = "draft"
	InvoicePlanStatusActive    InvoicePlanStatus = "active"
	InvoicePlanStatusCancelled InvoicePlanStatus = "cancelled"
)

// InvoicePlan is the Finance-built termin schedule for a quotation.
// Items sum to ~total (within tolerance_pct). Once activated, each
// item becomes an invoice in turn.
type InvoicePlan struct {
	ID             uuid.UUID
	QuotationID    uuid.UUID
	OpportunityID  uuid.UUID
	BOQVersionID   uuid.UUID
	PlanNumber     string
	Status         InvoicePlanStatus
	TotalAmount    float64 // grand total (subtotal + tax) the customer will pay
	SubtotalAmount float64
	TaxPct         float64
	TaxAmount      float64
	PlannedAmount  float64 // sum of items.amount — denormalized for fast tolerance check
	Currency       string
	TolerancePct   float64 // |total - planned|/total must be <= this
	Notes          string
	CreatedBy      *uuid.UUID
	Revision       int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type InvoicePlanItem struct {
	ID            uuid.UUID
	PlanID        uuid.UUID
	SeqNo         int
	Label         string
	Amount        float64
	DueOffsetDays int
	InvoiceID     *uuid.UUID
	IssuedAt      *time.Time
	Notes         string
	CreatedAt     time.Time
}

// NewInvoicePlan seeds a draft plan from a quotation snapshot.
func NewInvoicePlan(
	quotationID, opportunityID, boqVersionID uuid.UUID,
	planNumber string,
	totalAmount, subtotal, taxPct float64,
	currency string,
) (*InvoicePlan, error) {
	if totalAmount <= 0 {
		return nil, derrors.Validation(
			"invoice_plan.total_must_be_positive",
			"plan total must be > 0",
		)
	}
	if currency == "" {
		currency = "IDR"
	}
	if taxPct < 0 {
		taxPct = 0
	}
	taxAmount := 0.0
	if subtotal > 0 {
		taxAmount = subtotal * (taxPct / 100.0)
	}
	now := time.Now().UTC()
	return &InvoicePlan{
		ID:             uuid.New(),
		QuotationID:    quotationID,
		OpportunityID:  opportunityID,
		BOQVersionID:   boqVersionID,
		PlanNumber:     planNumber,
		Status:         InvoicePlanStatusDraft,
		TotalAmount:    totalAmount,
		SubtotalAmount: subtotal,
		TaxPct:         taxPct,
		TaxAmount:      taxAmount,
		Currency:       currency,
		TolerancePct:   0.5,
		Revision:       1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Activate flips the plan to active. Edge #9 / FN-1: enforce that the
// item sum equals the total within tolerance_pct.
func (p *InvoicePlan) Activate(items []InvoicePlanItem) error {
	if p.Status != InvoicePlanStatusDraft {
		return derrors.Conflict(
			"invoice_plan.invalid_transition",
			"only draft plans can be activated",
		)
	}
	if len(items) == 0 {
		return derrors.Validation(
			"invoice_plan.no_items",
			"plan must have at least one termin item",
		)
	}
	sum := 0.0
	for _, it := range items {
		sum += it.Amount
	}
	tol := p.TotalAmount * (p.TolerancePct / 100.0)
	if diff := absFloat(sum - p.TotalAmount); diff > tol {
		return derrors.Validation(
			"invoice_plan.tolerance_violation",
			fmt.Sprintf(
				"termin sum (%.2f) does not match plan total (%.2f) within tolerance (%.2f%%)",
				sum, p.TotalAmount, p.TolerancePct,
			),
		)
	}
	p.PlannedAmount = sum
	p.Status = InvoicePlanStatusActive
	return nil
}

// Cancel — terminal, drops all unissued items. Items already linked to
// issued invoices keep their linkage but the plan won't issue more.
func (p *InvoicePlan) Cancel(reason string) error {
	if p.Status == InvoicePlanStatusCancelled {
		return derrors.Conflict("invoice_plan.already_cancelled", "plan already cancelled")
	}
	p.Status = InvoicePlanStatusCancelled
	p.Notes = "cancelled: " + reason + " | " + p.Notes
	return nil
}

func NewInvoicePlanItem(planID uuid.UUID, seq int, label string, amount float64, dueDays int) (*InvoicePlanItem, error) {
	if seq < 1 {
		return nil, derrors.Validation("invoice_plan_item.seq_invalid", "seq_no must be >= 1")
	}
	if amount <= 0 {
		return nil, derrors.Validation("invoice_plan_item.amount_invalid", "amount must be > 0")
	}
	if dueDays < 0 {
		dueDays = 0
	}
	return &InvoicePlanItem{
		ID:            uuid.New(),
		PlanID:        planID,
		SeqNo:         seq,
		Label:         label,
		Amount:        amount,
		DueOffsetDays: dueDays,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

func GenerateInvoicePlanNumber(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf("PLAN-%s-%s",
		now.UTC().Format("20060102"),
		uuid.New().String()[:8],
	)
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
