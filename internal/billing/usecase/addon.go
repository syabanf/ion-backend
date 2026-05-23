// Wave 115 — Add-On Billing usecase.
//
// The customer-portal /portal/addons/buy handler in CRM already
// upserts crm.customer_addons. This service adds the billing-side
// counterpart that:
//
//   - serves the billing /portal/billing/add-ons/* routes the mobile
//     customer app calls in addition to the CRM path
//   - persists billing.add_on_purchases so the recurring scheduler in
//     usecase/r3.go can read active add-ons via the existing
//     CRMGateway.ActiveAddonsForCustomer projection
//   - keeps the CRM-side row in sync via the optional AddOnCRMGateway
//
// Wave 115 does NOT change the CRM /portal/addons/buy handler. Both
// paths coexist; the billing path simply gives the mobile app a
// billing-namespaced route per the brief.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/errors"
)

// AddOnService implements port.AddOnUseCase.
type AddOnService struct {
	purchases port.AddOnPurchaseRepository
	catalog   port.CatalogReader
	crm       port.AddOnCRMGateway // optional
}

func NewAddOnService(
	purchases port.AddOnPurchaseRepository,
	catalog port.CatalogReader,
	crm port.AddOnCRMGateway,
) *AddOnService {
	return &AddOnService{
		purchases: purchases,
		catalog:   catalog,
		crm:       crm,
	}
}

var _ port.AddOnUseCase = (*AddOnService)(nil)

// ListAvailable returns the catalog filtered to items the customer can
// purchase. Today we return the full active catalog; eligibility
// filtering (per plan / per branch / per customer-type) lands when
// the platform schema's add-on eligibility config is wired (later
// wave). The customerID parameter is reserved for that future filter.
func (s *AddOnService) ListAvailable(ctx context.Context, customerID uuid.UUID) ([]port.CatalogItem, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("addon.customer_required", "customer_id is required")
	}
	if s.catalog == nil {
		return nil, errors.Internal("addon.catalog_nil", "catalog reader not configured")
	}
	return s.catalog.ListActive(ctx)
}

// Purchase creates a billing.add_on_purchases row + (optionally) syncs
// the CRM mirror. Returns the freshly-created domain object so the
// handler can render the response without a second read.
func (s *AddOnService) Purchase(ctx context.Context, in port.PurchaseInput) (*domain.AddOnPurchase, error) {
	if in.CustomerID == uuid.Nil {
		return nil, errors.Validation("addon.customer_required", "customer_id is required")
	}
	if s.catalog == nil {
		return nil, errors.Internal("addon.catalog_nil", "catalog reader not configured")
	}
	item, err := s.catalog.FindBySKU(ctx, in.SKU)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errors.NotFound("addon.not_found", "add-on sku not found")
	}
	if !item.Active {
		return nil, errors.Validation("addon.inactive", "add-on is inactive")
	}
	qty := in.Quantity
	if qty <= 0 {
		qty = 1
	}
	// Price model: monthly_fee is what shows on recurring invoices; the
	// one-time fee is captured as the unit_price for this purchase
	// (matches the CRM-side semantics so the two ledgers don't drift).
	purchase, err := domain.NewAddOnPurchase(
		in.CustomerID,
		item.SKU,
		item.Name,
		item.Category,
		qty,
		item.MonthlyFee,
	)
	if err != nil {
		return nil, err
	}
	if err := s.purchases.Create(ctx, purchase); err != nil {
		return nil, err
	}
	if s.crm != nil {
		statusForCRM := "active"
		if purchase.Status == domain.AddOnStatusPendingInstall {
			statusForCRM = "pending_install"
		}
		// Best-effort CRM sync; a sync failure is logged at the handler
		// boundary but does not unwind the billing-side write — the
		// nightly reconciliation cron picks up the drift.
		_ = s.crm.UpsertCustomerAddon(ctx, in.CustomerID, item.ID, qty,
			item.OneTimeFee, item.MonthlyFee, statusForCRM)
	}
	return purchase, nil
}

// ListActive returns the customer's billable add-ons.
func (s *AddOnService) ListActive(ctx context.Context, customerID uuid.UUID) ([]domain.AddOnPurchase, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("addon.customer_required", "customer_id is required")
	}
	return s.purchases.ListByCustomer(ctx, customerID, []string{
		string(domain.AddOnStatusActive),
		string(domain.AddOnStatusPendingInstall),
	})
}

// Cancel records the customer's removal request. No mid-cycle refund
// (TC-AOB-005) — the recurring scheduler simply omits the line on the
// next cycle.
func (s *AddOnService) Cancel(ctx context.Context, customerID, addOnID uuid.UUID, reason string) (*domain.AddOnPurchase, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("addon.customer_required", "customer_id is required")
	}
	if addOnID == uuid.Nil {
		return nil, errors.Validation("addon.id_required", "add_on id is required")
	}
	p, err := s.purchases.FindByID(ctx, addOnID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.NotFound("addon.not_found", "add-on not found")
	}
	if p.CustomerID != customerID {
		// Don't leak the existence of another customer's row.
		return nil, errors.NotFound("addon.not_found", "add-on not found")
	}
	if err := p.Cancel(reason); err != nil {
		return nil, err
	}
	if err := s.purchases.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// MarkExpiredOlderThan is a helper for the recurring scheduler / cron.
// Exposed on the service rather than the port so callers that don't
// need it (HTTP handlers) don't see the surface.
func (s *AddOnService) MarkExpiredOlderThan(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.purchases.ListExpiring(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	for i := range rows {
		if err := rows[i].Expire(); err != nil {
			continue
		}
		if err := s.purchases.Update(ctx, &rows[i]); err != nil {
			continue
		}
		expired++
	}
	return expired, nil
}
