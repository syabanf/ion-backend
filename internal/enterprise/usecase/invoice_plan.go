package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithInvoicePlans attaches the E5 termin schedule repo.
func (s *Service) WithInvoicePlans(repo port.InvoicePlanRepository) *Service {
	s.invoicePlans = repo
	return s
}

func (s *Service) GetInvoicePlanByQuotation(ctx context.Context, quotationID uuid.UUID) (*domain.InvoicePlan, []domain.InvoicePlanItem, error) {
	if s.invoicePlans == nil {
		return nil, nil, errFinanceNotConfigured()
	}
	p, err := s.invoicePlans.FindByQuotationID(ctx, quotationID)
	if err != nil {
		return nil, nil, err
	}
	items, err := s.invoicePlans.ListItems(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	return p, items, nil
}

func (s *Service) GetInvoicePlan(ctx context.Context, id uuid.UUID) (*domain.InvoicePlan, []domain.InvoicePlanItem, error) {
	if s.invoicePlans == nil {
		return nil, nil, errFinanceNotConfigured()
	}
	p, err := s.invoicePlans.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	items, err := s.invoicePlans.ListItems(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	return p, items, nil
}

// CreateInvoicePlan seeds a draft from the source quotation. Pulls the
// tax breakdown from the BOQ so the plan carries it forward to each
// issued invoice (E5 + E6 working together).
func (s *Service) CreateInvoicePlan(ctx context.Context, in port.CreateInvoicePlanInput) (*domain.InvoicePlan, error) {
	if s.invoicePlans == nil || s.quotations == nil {
		return nil, errFinanceNotConfigured()
	}
	// Idempotent — same quotation produces one plan.
	if existing, err := s.invoicePlans.FindByQuotationID(ctx, in.QuotationID); err == nil {
		return existing, nil
	} else if !derrors.IsNotFound(err) {
		return nil, err
	}
	q, err := s.quotations.FindByID(ctx, in.QuotationID)
	if err != nil {
		return nil, err
	}
	subtotal := 0.0
	taxPct := domain.DefaultTaxPct
	if s.boqs != nil {
		if b, err := s.boqs.FindByID(ctx, q.BOQVersionID); err == nil && b != nil {
			subtotal = b.SubtotalAmount
			if b.TaxPct > 0 {
				taxPct = b.TaxPct
			}
		}
	}
	plan, err := domain.NewInvoicePlan(
		q.ID, q.OpportunityID, q.BOQVersionID,
		domain.GenerateInvoicePlanNumber(time.Now()),
		q.SellTotal, subtotal, taxPct,
		q.Currency,
	)
	if err != nil {
		return nil, err
	}
	plan.Notes = strings.TrimSpace(in.Notes)
	plan.CreatedBy = in.CreatedBy
	if err := s.invoicePlans.Create(ctx, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// ReplaceInvoicePlanItems sets the termin schedule on a DRAFT plan.
// Activation is a separate step (so Finance can review before locking).
func (s *Service) ReplaceInvoicePlanItems(ctx context.Context, in port.ReplaceInvoicePlanItemsInput) ([]domain.InvoicePlanItem, error) {
	if s.invoicePlans == nil {
		return nil, errFinanceNotConfigured()
	}
	p, err := s.invoicePlans.FindByID(ctx, in.PlanID)
	if err != nil {
		return nil, err
	}
	if p.Status != domain.InvoicePlanStatusDraft {
		return nil, derrors.Conflict(
			"invoice_plan.not_draft",
			"can only edit items while plan is draft",
		)
	}
	items := make([]domain.InvoicePlanItem, 0, len(in.Items))
	for _, raw := range in.Items {
		it, err := domain.NewInvoicePlanItem(p.ID, raw.SeqNo, raw.Label, raw.Amount, raw.DueOffsetDays)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	if err := s.invoicePlans.ReplaceItems(ctx, p.ID, items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) ActivateInvoicePlan(ctx context.Context, in port.ActivateInvoicePlanInput) (*domain.InvoicePlan, error) {
	if s.invoicePlans == nil {
		return nil, errFinanceNotConfigured()
	}
	p, err := s.invoicePlans.FindByID(ctx, in.PlanID)
	if err != nil {
		return nil, err
	}
	items, err := s.invoicePlans.ListItems(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if err := p.Activate(items); err != nil {
		return nil, err
	}
	if err := s.invoicePlans.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// IssueTerminItem turns a single termin into a live invoice. The invoice
// carries plan_id + plan_item_id back-refs so the schedule audit is
// preserved.
func (s *Service) IssueTerminItem(ctx context.Context, in port.IssueTerminItemInput) (*domain.Invoice, error) {
	if s.invoicePlans == nil || s.invoices == nil {
		return nil, errFinanceNotConfigured()
	}
	it, err := s.invoicePlans.FindItemByID(ctx, in.ItemID)
	if err != nil {
		return nil, err
	}
	if it.InvoiceID != nil {
		// Already issued — idempotent return.
		inv, err := s.invoices.FindByID(ctx, *it.InvoiceID)
		if err != nil {
			return nil, err
		}
		return inv, nil
	}
	plan, err := s.invoicePlans.FindByID(ctx, it.PlanID)
	if err != nil {
		return nil, err
	}
	if plan.Status != domain.InvoicePlanStatusActive {
		return nil, derrors.Conflict(
			"invoice_plan.not_active",
			"can only issue termin invoices from an active plan",
		)
	}
	// Build a per-termin invoice. Tax math: scale the plan's tax rate to
	// the termin amount. (We treat termin amounts as gross.)
	termGross := it.Amount
	termSubtotal := termGross
	if plan.TaxPct > 0 {
		termSubtotal = termGross / (1 + plan.TaxPct/100.0)
	}
	due := time.Now().UTC().AddDate(0, 0, it.DueOffsetDays)
	inv, err := domain.NewInvoice(
		plan.QuotationID, plan.OpportunityID, plan.BOQVersionID,
		domain.GenerateInvoiceNumber(time.Now()),
		termGross, termSubtotal, plan.TaxPct,
		plan.Currency, due,
	)
	if err != nil {
		return nil, err
	}
	inv.IssuedBy = in.IssuedBy
	inv.InvoicePlanID = &plan.ID
	inv.InvoicePlanItemID = &it.ID
	inv.Notes = it.Label
	if err := s.invoices.Create(ctx, inv); err != nil {
		return nil, err
	}
	// Link item back to invoice.
	now := time.Now().UTC()
	it.InvoiceID = &inv.ID
	it.IssuedAt = &now
	if err := s.invoicePlans.UpdateItem(ctx, it); err != nil {
		if s.log != nil {
			s.log.Warn("plan item linkback failed",
				"item_id", it.ID.String(), "err", err.Error())
		}
	}
	return inv, nil
}

func (s *Service) CancelInvoicePlan(ctx context.Context, in port.CancelInvoicePlanInput) (*domain.InvoicePlan, error) {
	if s.invoicePlans == nil {
		return nil, errFinanceNotConfigured()
	}
	p, err := s.invoicePlans.FindByID(ctx, in.PlanID)
	if err != nil {
		return nil, err
	}
	if err := p.Cancel(in.Reason); err != nil {
		return nil, err
	}
	if err := s.invoicePlans.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}
