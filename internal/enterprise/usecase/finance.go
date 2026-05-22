package usecase

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithFinance wires Phase 5 repos onto the Service. Same nil-safe
// pattern as WithBOQ / WithQuotations / WithNegotiation — finance
// methods return errFinanceNotConfigured when any one is missing.
func (s *Service) WithFinance(
	invoices port.InvoiceRepository,
	payments port.InvoicePaymentRepository,
	ewos port.EWORepository,
) *Service {
	s.invoices = invoices
	s.invoicePayments = payments
	s.ewos = ewos
	return s
}

func errFinanceNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "finance.not_configured",
		"finance surface is not configured for this service", nil)
}

// =====================================================================
// Invoices
// =====================================================================

func (s *Service) ListInvoices(ctx context.Context, f port.InvoiceListFilter) ([]domain.Invoice, int, error) {
	if s.invoices == nil {
		return nil, 0, errFinanceNotConfigured()
	}
	return s.invoices.List(ctx, f)
}

func (s *Service) GetInvoice(ctx context.Context, id uuid.UUID) (*domain.Invoice, []domain.InvoicePayment, error) {
	if s.invoices == nil {
		return nil, nil, errFinanceNotConfigured()
	}
	inv, err := s.invoices.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	payments, err := s.invoicePayments.ListByInvoice(ctx, inv.ID)
	if err != nil {
		return nil, nil, err
	}
	return inv, payments, nil
}

// IssueInvoice creates a sealed invoice snapshot from a quotation. The
// caller MUST pass a quotation that's accepted; we don't gate on it
// here because the usual call site is AutoCreateOnQuotationAccept (which
// just transitioned the quote to accepted). The manual path through the
// HTTP layer is intentionally open so an operator can re-issue if the
// auto-hook didn't fire.
//
// Idempotency: if an invoice already exists for the quotation, return it.
// (TC-IN-001 — same quotation never produces two invoices.)
func (s *Service) IssueInvoice(ctx context.Context, in port.IssueInvoiceInput) (*domain.Invoice, error) {
	if s.invoices == nil || s.quotations == nil {
		return nil, errFinanceNotConfigured()
	}
	// Idempotency check.
	if existing, err := s.invoices.FindByQuotationID(ctx, in.QuotationID); err == nil {
		return existing, nil
	} else if !derrors.IsNotFound(err) {
		return nil, err
	}

	q, err := s.quotations.FindByID(ctx, in.QuotationID)
	if err != nil {
		return nil, err
	}
	// Only accepted quotations should produce invoices. This is a soft
	// guard; we don't enforce here because there are valid "issue early"
	// scenarios in field workflows.
	if q.Status != domain.QuotationStatusAccepted {
		return nil, derrors.Conflict(
			"invoice.quotation_not_accepted",
			"quotation must be accepted before invoicing",
		)
	}

	dueDays := in.DueDays
	if dueDays <= 0 {
		dueDays = 30
	}
	due := time.Now().UTC().AddDate(0, 0, dueDays)

	// Pull tax breakdown from the source BOQ so the invoice carries
	// subtotal + tax_pct + tax_amount (E6 / FN-1). Quotation.SellTotal is
	// the GRAND total which equals BOQ.SellTotal; subtotal lives on the
	// BOQ header now. If the BOQ predates migration 0030, subtotal will
	// be 0 and we fall back to legacy mode.
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
	inv, err := domain.NewInvoice(
		q.ID, q.OpportunityID, q.BOQVersionID,
		domain.GenerateInvoiceNumber(time.Now()),
		q.SellTotal, subtotal, taxPct,
		q.Currency,
		due,
	)
	if err != nil {
		return nil, err
	}
	inv.IssuedBy = in.IssuedBy
	inv.Notes = strings.TrimSpace(in.Notes)
	if err := s.invoices.Create(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// VoidInvoice flips an invoice to voided. Allowed from any non-paid,
// non-voided state. Uses optimistic concurrency via IfRevision.
func (s *Service) VoidInvoice(ctx context.Context, in port.VoidInvoiceInput) (*domain.Invoice, error) {
	if s.invoices == nil {
		return nil, errFinanceNotConfigured()
	}
	inv, err := s.invoices.FindByID(ctx, in.InvoiceID)
	if err != nil {
		return nil, err
	}
	if err := inv.Void(in.Reason); err != nil {
		return nil, err
	}
	if err := s.invoices.Update(ctx, inv, in.IfRevision); err != nil {
		return nil, err
	}
	return inv, nil
}

// =====================================================================
// Payments
// =====================================================================

func (s *Service) RecordPayment(ctx context.Context, in port.RecordPaymentInput) (*domain.InvoicePayment, *domain.Invoice, error) {
	if s.invoices == nil {
		return nil, nil, errFinanceNotConfigured()
	}
	inv, err := s.invoices.FindByID(ctx, in.InvoiceID)
	if err != nil {
		return nil, nil, err
	}

	paidAt := time.Now().UTC()
	if in.PaidAt != "" {
		if t, perr := time.Parse(time.RFC3339, in.PaidAt); perr == nil {
			paidAt = t
		} else if t, perr := time.Parse("2006-01-02", in.PaidAt); perr == nil {
			paidAt = t
		} else {
			return nil, nil, derrors.Validation(
				"payment.paid_at_invalid",
				"paid_at must be RFC 3339 or YYYY-MM-DD",
			)
		}
	}

	p, err := domain.NewInvoicePayment(
		inv.ID, in.Amount, in.Method, in.Reference, in.Notes, paidAt,
	)
	if err != nil {
		return nil, nil, err
	}
	p.RecordedBy = in.RecordedBy
	if err := inv.ApplyPayment(in.Amount); err != nil {
		return nil, nil, err
	}
	// Two writes; we accept a brief window where payment row exists but
	// invoice hasn't flipped yet. The aggregate paid_amount is the cache,
	// the ledger rows are truth — a future polish could wrap this in a
	// tx, but the per-deal cadence makes the race vanishingly rare.
	if err := s.invoicePayments.Create(ctx, p); err != nil {
		return nil, nil, err
	}
	if err := s.invoices.Update(ctx, inv, nil); err != nil {
		return nil, nil, err
	}
	return p, inv, nil
}

// =====================================================================
// EWOs
// =====================================================================

func (s *Service) ListEWOs(ctx context.Context, f port.EWOListFilter) ([]domain.EWO, int, error) {
	if s.ewos == nil {
		return nil, 0, errFinanceNotConfigured()
	}
	return s.ewos.List(ctx, f)
}

func (s *Service) GetEWO(ctx context.Context, id uuid.UUID) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	return s.ewos.FindByID(ctx, id)
}

func (s *Service) CreateEWO(ctx context.Context, in port.CreateEWOInput) (*domain.EWO, error) {
	if s.ewos == nil || s.quotations == nil {
		return nil, errFinanceNotConfigured()
	}
	// Idempotency: one EWO per quotation (TC-EWO-001).
	if existing, err := s.ewos.FindByQuotationID(ctx, in.QuotationID); err == nil {
		return existing, nil
	} else if !derrors.IsNotFound(err) {
		return nil, err
	}
	q, err := s.quotations.FindByID(ctx, in.QuotationID)
	if err != nil {
		return nil, err
	}
	if q.Status != domain.QuotationStatusAccepted {
		return nil, derrors.Conflict(
			"ewo.quotation_not_accepted",
			"quotation must be accepted before EWO creation",
		)
	}
	e, err := domain.NewEWO(
		q.ID, q.OpportunityID, q.BOQVersionID,
		domain.GenerateEWONumber(time.Now()),
		strings.TrimSpace(in.Notes),
	)
	if err != nil {
		return nil, err
	}
	if err := s.ewos.Create(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Service) AssignEWO(ctx context.Context, in port.AssignEWOInput) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, in.EWOID)
	if err != nil {
		return nil, err
	}
	if err := e.Assign(in.AssignedTo); err != nil {
		return nil, err
	}
	if err := s.ewos.Update(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Service) StartEWO(ctx context.Context, id uuid.UUID) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := e.Start(); err != nil {
		return nil, err
	}
	if err := s.ewos.Update(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Service) CompleteEWO(ctx context.Context, id uuid.UUID) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := e.Complete(); err != nil {
		return nil, err
	}
	if err := s.ewos.Update(ctx, e); err != nil {
		return nil, err
	}
	// Best-effort: log the completion so the vendor-metrics derivation
	// cron has fresh material. A failure here doesn't revert the
	// completion — the daily backfill in the migration plus a future
	// re-run will eventually pick it up.
	_ = s.ewos.LogCompletion(ctx, e.ID)
	return e, nil
}

func (s *Service) CancelEWO(ctx context.Context, in port.CancelEWOInput) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, in.EWOID)
	if err != nil {
		return nil, err
	}
	if err := e.Cancel(in.Reason); err != nil {
		return nil, err
	}
	if err := s.ewos.Update(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// =====================================================================
// AutoCreateOnQuotationAccept — the quotation-accept hook entry point.
// =====================================================================

// AutoCreateOnQuotationAccept is invoked from AcceptQuotation. It
// best-effort creates an invoice + EWO for the just-accepted quotation.
// Both flows are individually idempotent so a re-fire is safe.
//
// Errors are logged + swallowed so a Phase 5 hiccup never reverts the
// quotation acceptance. The operator can manually issue/create via the
// HTTP endpoints if the auto-fire misses.
func (s *Service) AutoCreateOnQuotationAccept(ctx context.Context, quotationID uuid.UUID) error {
	if s.invoices == nil || s.ewos == nil {
		// Phase 5 not wired — nothing to do.
		return nil
	}
	if _, err := s.IssueInvoice(ctx, port.IssueInvoiceInput{
		QuotationID: quotationID,
		DueDays:     30,
	}); err != nil {
		if s.log != nil {
			s.log.Warn("auto-issue invoice failed",
				"quotation_id", quotationID.String(),
				"err", err.Error(),
			)
		}
	}
	if _, err := s.CreateEWO(ctx, port.CreateEWOInput{
		QuotationID: quotationID,
		Notes:       "Auto-created on quotation acceptance.",
	}); err != nil {
		if s.log != nil {
			s.log.Warn("auto-create ewo failed",
				"quotation_id", quotationID.String(),
				"err", err.Error(),
			)
		}
	}
	return nil
}

// silence: errors used for derrors.IsNotFound matching above
var _ = errors.New
