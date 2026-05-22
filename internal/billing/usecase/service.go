// Package usecase implements the billing context's business rules.
//
// Core flow (round 1):
//
//	CreateInvoice(in)   → builds invoice + lines; status starts 'draft'
//	                      or 'issued' (IssueImmediately=true for auto-OTC).
//	IssueInvoice(id)    → draft → issued.
//	RecordPayment(in)   → write payment row; if sum_of_confirmed ≥ total,
//	                      flip invoice to 'paid' with paid_at=now.
//	IsOrderOTCPaid(...) → cross-context check used by M5's BAST verify.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Service struct {
	invoices port.InvoiceRepository
	payments port.PaymentRepository

	// M6 r2 — optional, nil-safe.
	policies    port.PolicyRepository
	cycles      port.CycleRepository
	commissions port.CommissionRepository
	crm         port.CRMGateway
	network     port.NetworkGateway
	log         *slog.Logger

	// M6 r3 — voluntary termination, auto-termination follow-through,
	// referral reward. Nil-safe.
	terminations port.TerminationRequestRepository
	rewards      port.ReferralRewardRepository
	field        port.FieldGateway

	// M6 r3 — customer-portal OTP for self-service termination.
	portalOTPs     port.CustomerOTPRepository
	portalLookup   port.CustomerLookupGateway
	includeDevOTP  bool

	// M6 r4 — platform Schema System v1 resolver. Optional; when wired
	// the billing tick / commission calc read their config from
	// per-customer schemas instead of the global billing.policies row.
	// Nil-safe: the schema_policy.go bridge handles a nil resolver by
	// always falling back to the legacy values.
	schemaPolicy *schemaPolicyResolver
}

func NewService(invoices port.InvoiceRepository, payments port.PaymentRepository) *Service {
	return &Service{invoices: invoices, payments: payments}
}

var _ port.UseCase = (*Service)(nil)

// CreateInvoice builds a draft (or issued) invoice from caller-supplied lines.
// Round-1 OTC auto-creation from CRM sets IssueImmediately=true so the
// finance flow can mark it paid without first issuing.
func (s *Service) CreateInvoice(ctx context.Context, in port.CreateInvoiceInput) (*port.InvoiceView, error) {
	lines := make([]domain.LineItem, 0, len(in.Lines))
	for _, li := range in.Lines {
		amt := li.UnitPrice * li.Quantity
		if li.Quantity == 0 {
			amt = li.UnitPrice
		}
		lines = append(lines, domain.LineItem{
			Description: li.Description,
			ItemType:    li.ItemType,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			Amount:      amt,
		})
	}
	inv, persistLines, err := domain.NewInvoice(
		in.CustomerID, in.OrderID, in.InvoiceType,
		lines, in.PPNRate, in.DueDate, in.CreatedBy, in.Notes,
	)
	if err != nil {
		return nil, err
	}
	if in.IssueImmediately {
		if err := inv.Issue(); err != nil {
			return nil, err
		}
	}
	if err := s.invoices.Create(ctx, inv, persistLines); err != nil {
		return nil, err
	}
	return s.invoices.FindByID(ctx, inv.ID)
}

func (s *Service) IssueInvoice(ctx context.Context, id uuid.UUID) (*port.InvoiceView, error) {
	v, err := s.invoices.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := v.Invoice.Issue(); err != nil {
		return nil, err
	}
	if err := s.invoices.UpdateStatus(ctx, id, v.Invoice.Status, nil); err != nil {
		return nil, err
	}
	return s.invoices.FindByID(ctx, id)
}

func (s *Service) CancelInvoice(ctx context.Context, id uuid.UUID) (*port.InvoiceView, error) {
	v, err := s.invoices.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := v.Invoice.Cancel(); err != nil {
		return nil, err
	}
	if err := s.invoices.UpdateStatus(ctx, id, v.Invoice.Status, nil); err != nil {
		return nil, err
	}
	return s.invoices.FindByID(ctx, id)
}

func (s *Service) GetInvoice(ctx context.Context, id uuid.UUID) (*port.InvoiceView, error) {
	return s.invoices.FindByID(ctx, id)
}

func (s *Service) ListInvoices(ctx context.Context, f port.InvoiceListFilter) ([]port.InvoiceView, int, error) {
	return s.invoices.List(ctx, f)
}

// RecordPayment writes a confirmed payment and, when payments now cover
// the invoice total, transitions the invoice to 'paid'.
//
// We do NOT collapse this into a single tx with the status update because
// the recompute pulls the SUM via a separate query (and Postgres's read
// committed isolation already gives us the consistency we need at the
// scale we care about). If we ever face concurrent payment posting we
// can lift this into a tx with SELECT ... FOR UPDATE on the invoice row.
func (s *Service) RecordPayment(ctx context.Context, in port.RecordPaymentInput) (*port.InvoiceView, error) {
	inv, err := s.invoices.FindByID(ctx, in.InvoiceID)
	if err != nil {
		return nil, err
	}
	if inv.Invoice.Status == domain.InvoiceStatusCancelled {
		return nil, derrors.Conflict("invoice.cancelled",
			"cannot record payment on a cancelled invoice")
	}
	if inv.Invoice.Status == domain.InvoiceStatusPaid {
		return nil, derrors.Conflict("invoice.already_paid",
			"invoice already fully paid")
	}

	p, err := domain.NewPayment(in.InvoiceID, inv.Invoice.CustomerID, in.Amount,
		in.PaymentMethod, &in.ConfirmedBy, in.Notes)
	if err != nil {
		return nil, err
	}
	p.GatewayTransactionID = in.GatewayTransactionID
	if err := s.payments.Create(ctx, p); err != nil {
		return nil, err
	}

	// Recompute and flip to paid if covered.
	sum, err := s.payments.SumConfirmedForInvoice(ctx, in.InvoiceID)
	if err != nil {
		return nil, err
	}
	flippedToPaid := false
	if sum+1e-6 >= inv.Invoice.Total {
		now := time.Now().UTC()
		if err := s.invoices.UpdateStatus(ctx, in.InvoiceID, domain.InvoiceStatusPaid, &now); err != nil {
			return nil, err
		}
		flippedToPaid = true
	}

	updated, err := s.invoices.FindByID(ctx, in.InvoiceID)
	if err != nil {
		return nil, err
	}

	// M6 r2 — hook commission calc when this payment fully paid the OTC.
	// maybeApplyCommission is nil-safe when WithR2 isn't wired.
	if flippedToPaid {
		s.maybeApplyCommission(ctx, updated, p)
		// M6 r3 — referral reward (nil-safe when r3 unwired).
		s.maybeApplyReferralReward(ctx, updated, p)
	}

	return updated, nil
}

// IsOrderOTCPaid is the cross-context check the field service uses to
// gate NOC approval. Returns:
//
//	(true, nil)  — OTC invoice exists for this order AND status=paid
//	(false, nil) — OTC invoice exists but not paid
//	(true, nil)  — NO OTC invoice exists for this order; we treat this as
//	               "no gate" — operations may still install for orders
//	               that never had an OTC (e.g., promo free install). When
//	               OTC auto-creation is on for every order, this branch is
//	               unreachable; we keep it as a safety hatch.
func (s *Service) IsOrderOTCPaid(ctx context.Context, orderID uuid.UUID) (bool, error) {
	inv, err := s.invoices.FindOTCForOrder(ctx, orderID)
	if err != nil {
		return false, err
	}
	if inv == nil {
		return true, nil
	}
	return inv.Invoice.Status == domain.InvoiceStatusPaid, nil
}
