package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
)

// =====================================================================
// Wave 115 — Add-On Billing
//
// The add-on lifecycle straddles three places:
//   * crm.product_addons  — catalog (read-only here)
//   * crm.customer_addons — CRM ledger (the existing /portal/addons/buy
//                           handler maintains this)
//   * billing.add_on_purchases — billing-side mirror (Wave 115 introduces
//                                 this so the recurring scheduler + the
//                                 read side can show charges per cycle)
//
// This port keeps the surfaces narrow: the catalog read, the purchase
// write, the active list, the cancel write. The AddOnService glues
// them together.
// =====================================================================

// CatalogItem is the public catalog row a customer sees when browsing
// available add-ons. Mirrors the columns the CRM admin maintains.
type CatalogItem struct {
	ID              uuid.UUID
	SKU             string
	Name            string
	Category        domain.AddOnCategory
	OneTimeFee      float64
	MonthlyFee      float64
	RequiresInstall bool
	Active          bool
}

// PurchaseInput is what the customer-portal /portal/billing/add-ons/
// purchase route hands in. CustomerID arrives from claims, never the
// body.
type PurchaseInput struct {
	CustomerID uuid.UUID
	SKU        string
	Quantity   int
	Notes      string
}

// PurchaseRepository is the persistence boundary for billing.add_on_
// purchases. The CRM catalog + crm.customer_addons are read/written via
// a separate gateway (CatalogReader / AddOnCRMGateway) so the cross-
// context coupling stays explicit.
type AddOnPurchaseRepository interface {
	Create(ctx context.Context, p *domain.AddOnPurchase) error
	Update(ctx context.Context, p *domain.AddOnPurchase) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.AddOnPurchase, error)
	ListByCustomer(ctx context.Context, customerID uuid.UUID, statuses []string) ([]domain.AddOnPurchase, error)
	ListExpiring(ctx context.Context, before time.Time, limit int) ([]domain.AddOnPurchase, error)
}

// CatalogReader is the cross-context read into crm.product_addons. SQL-
// only so the billing module doesn't take a Go dependency on crm.
type CatalogReader interface {
	ListActive(ctx context.Context) ([]CatalogItem, error)
	FindBySKU(ctx context.Context, sku string) (*CatalogItem, error)
}

// AddOnCRMGateway syncs the CRM-side customer_addons row so the
// existing crm/portal_auth.go::buyAddon path and the new billing path
// don't diverge. Optional: when nil, the service skips the sync (used
// in tests).
type AddOnCRMGateway interface {
	UpsertCustomerAddon(
		ctx context.Context,
		customerID, addonID uuid.UUID,
		quantity int,
		oneTimeFee, monthlyFee float64,
		status string,
	) error
	MarkCancelled(ctx context.Context, customerAddonID uuid.UUID, reason string) error
}

// AddOnUseCase is the driving contract.
type AddOnUseCase interface {
	ListAvailable(ctx context.Context, customerID uuid.UUID) ([]CatalogItem, error)
	Purchase(ctx context.Context, in PurchaseInput) (*domain.AddOnPurchase, error)
	ListActive(ctx context.Context, customerID uuid.UUID) ([]domain.AddOnPurchase, error)
	Cancel(ctx context.Context, customerID, addOnID uuid.UUID, reason string) (*domain.AddOnPurchase, error)
}
