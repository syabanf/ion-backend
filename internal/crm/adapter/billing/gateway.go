// Package billing adapts the billing context's usecase to the CRM port.
// In-process today; swap to HTTP when billing splits to its own process.
package billing

import (
	"context"
	"log/slog"
	"time"

	billingdomain "github.com/ion-core/backend/internal/billing/domain"
	billingport "github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/internal/crm/port"
)

// BillingService is the subset of billing.usecase.Service we need.
type BillingService interface {
	CreateInvoice(ctx context.Context, in billingport.CreateInvoiceInput) (*billingport.InvoiceView, error)
}

type Gateway struct {
	svc BillingService
	log *slog.Logger
}

func NewGateway(svc BillingService) *Gateway {
	return &Gateway{svc: svc, log: slog.Default()}
}

var _ port.BillingGateway = (*Gateway)(nil)

// CreateOTCForOrder builds the OTC invoice from a CRM convert event.
//
// Gap B — OTC type dispatch (PRD §6.4):
//
//	free      — no invoice generated; we log an audit-only skip and
//	            return nil so conversion finishes cleanly. The order
//	            is still considered "contracted".
//	prepaid   — invoice issued eagerly; activation gates on payment.
//	            Same wire shape as the round-1 default.
//	postpaid  — invoice deferred to the activation hook (field-svc's
//	            BAST verify path). We don't create an invoice here; the
//	            activation hook calls CreateInvoice itself.
//	(empty)   — treated as 'postpaid' for back-compat with callers that
//	            pre-date Gap B.
//
// Invoice schema (free of `prepaid`):
//
//	Line 1 — Installation fee (otc), unit_price = OTCAmount
//	Line 2 — Excess cable (excess_cable), only when ExcessAmount > 0
//
// We issue immediately so it's visible to Finance + the BAST verify gate
// right away. PPN defaults to 11% (Indonesia standard); the billing
// usecase keeps the actual math.
func (g *Gateway) CreateOTCForOrder(ctx context.Context, in port.OTCRequest) error {
	otcType := in.OTCType
	if otcType == "" {
		otcType = "postpaid"
	}

	switch otcType {
	case "free":
		// Audit-only — no invoice. Finance can reconcile via the
		// order's otc_type column; the log line is the trace.
		g.log.InfoContext(ctx, "otc_free_skipped",
			slog.String("order_id", in.OrderID.String()),
			slog.String("customer_id", in.CustomerID.String()),
			slog.String("product", in.ProductLabel),
		)
		return nil

	case "postpaid":
		// Deferred to activation; nothing to do at conversion.
		g.log.InfoContext(ctx, "otc_postpaid_deferred",
			slog.String("order_id", in.OrderID.String()),
			slog.String("customer_id", in.CustomerID.String()),
		)
		return nil

	case "prepaid":
		// Fall through to the create-invoice path below.

	default:
		// Unknown type — fail closed via postpaid behaviour so a
		// misconfigured product doesn't silently create a phantom
		// invoice. The log line surfaces the misconfiguration.
		g.log.WarnContext(ctx, "otc_unknown_type_treated_as_postpaid",
			slog.String("order_id", in.OrderID.String()),
			slog.String("otc_type", in.OTCType),
		)
		return nil
	}

	due := time.Now().UTC().AddDate(0, 0, 7)
	lines := []billingport.LineItemInput{
		{
			Description: "Installation fee — " + in.ProductLabel,
			ItemType:    "otc",
			Quantity:    1,
			UnitPrice:   in.OTCAmount,
		},
	}
	if in.ExcessAmount > 0 {
		lines = append(lines, billingport.LineItemInput{
			Description: "Excess cable charge",
			ItemType:    "excess_cable",
			Quantity:    1,
			UnitPrice:   in.ExcessAmount,
		})
	}
	oid := in.OrderID
	_, err := g.svc.CreateInvoice(ctx, billingport.CreateInvoiceInput{
		CustomerID:       in.CustomerID,
		OrderID:          &oid,
		InvoiceType:      billingdomain.InvoiceTypeOTC,
		Lines:            lines,
		PPNRate:          11.0,
		DueDate:          due,
		IssueImmediately: true,
	})
	return err
}
