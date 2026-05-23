package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
)

// WholesaleService implements port.WholesaleUseCase. Catalog ops are
// admin-only; order ops are split between admin (approve / reject /
// fulfill) and platform (create / submit / cancel) but the service
// itself doesn't enforce that split — the HTTP layer chooses which
// methods it exposes per surface.
type WholesaleService struct {
	skus     port.WholesaleSKURepository
	orders   port.WholesaleOrderRepository
	accounts port.ResellerAccountRepository
}

func NewWholesaleService(skus port.WholesaleSKURepository, orders port.WholesaleOrderRepository, accounts port.ResellerAccountRepository) *WholesaleService {
	return &WholesaleService{skus: skus, orders: orders, accounts: accounts}
}

var _ port.WholesaleUseCase = (*WholesaleService)(nil)

// =====================================================================
// Catalog
// =====================================================================

func (s *WholesaleService) CreateSKU(ctx context.Context, in port.CreateWholesaleSKUInput) (*domain.WholesaleSKU, error) {
	sku, err := domain.NewWholesaleSKU(in.SupplierSubsidiaryID, in.Name, in.SKUCode, in.Unit, in.UnitPrice)
	if err != nil {
		return nil, err
	}
	if err := s.skus.Create(ctx, sku); err != nil {
		return nil, err
	}
	return sku, nil
}

func (s *WholesaleService) UpdateSKU(ctx context.Context, in port.UpdateWholesaleSKUInput) (*domain.WholesaleSKU, error) {
	sku, err := s.skus.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return nil, errors.Validation("sku.name_required", "name is required")
		}
		sku.Name = n
	}
	if in.UnitPrice != nil {
		if *in.UnitPrice < 0 {
			return nil, errors.Validation("sku.price_negative", "unit_price must be >= 0")
		}
		sku.UnitPrice = *in.UnitPrice
	}
	if in.Unit != nil {
		u := strings.TrimSpace(*in.Unit)
		if u == "" {
			u = "unit"
		}
		sku.Unit = u
	}
	if in.IsActive != nil {
		sku.IsActive = *in.IsActive
	}
	if err := s.skus.Update(ctx, sku); err != nil {
		return nil, err
	}
	return sku, nil
}

func (s *WholesaleService) ListSKUs(ctx context.Context, f port.WholesaleSKUListFilter) ([]domain.WholesaleSKU, int, error) {
	return s.skus.List(ctx, f)
}

// =====================================================================
// Orders
// =====================================================================

// CreateOrder builds a draft order from a SKU + qty list. The reseller
// id MUST already be set on the input (the platform middleware
// resolved it from the session token). We refuse:
//   - empty line list
//   - SKUs that don't exist or are inactive
//   - mixed-supplier orders (a single order routes to one supplier)
//   - reseller accounts that aren't approved
//
// All four checks happen before any DB write so a failed create
// doesn't leak a half-built header.
func (s *WholesaleService) CreateOrder(ctx context.Context, in port.CreateWholesaleOrderInput) (*domain.WholesaleOrder, error) {
	if in.ResellerAccountID == uuid.Nil {
		return nil, errors.Validation("order.reseller_required", "reseller_account_id is required")
	}
	if len(in.Lines) == 0 {
		return nil, errors.Validation("order.empty", "order must have at least one line")
	}
	acc, err := s.accounts.FindByID(ctx, in.ResellerAccountID)
	if err != nil {
		return nil, err
	}
	if !acc.IsOperational() {
		return nil, errors.Forbidden(
			"order.reseller_not_operational",
			fmt.Sprintf("reseller is %s — only approved resellers can place orders", acc.Status),
		)
	}

	// Resolve every SKU upfront so we can fail before mutating state.
	ids := make([]uuid.UUID, 0, len(in.Lines))
	for _, l := range in.Lines {
		ids = append(ids, l.SKUID)
	}
	skus, err := s.skus.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]domain.WholesaleSKU, len(skus))
	for _, sku := range skus {
		byID[sku.ID] = sku
	}
	for _, id := range ids {
		sku, ok := byID[id]
		if !ok {
			return nil, errors.Validation("order.sku_not_found", fmt.Sprintf("sku %s not found", id))
		}
		if !sku.IsActive {
			return nil, errors.Validation("order.sku_inactive", fmt.Sprintf("sku %s is inactive", sku.SKUCode))
		}
	}

	// Mixed-supplier check — first line's supplier wins, all others
	// must match.
	first := byID[in.Lines[0].SKUID]
	for _, l := range in.Lines[1:] {
		if byID[l.SKUID].SupplierSubsidiaryID != first.SupplierSubsidiaryID {
			return nil, errors.Validation(
				"order.mixed_supplier",
				"all lines must share the same supplier_subsidiary_id",
			)
		}
	}

	order, err := domain.NewWholesaleOrder(in.ResellerAccountID, first.SupplierSubsidiaryID)
	if err != nil {
		return nil, err
	}
	for _, l := range in.Lines {
		sku := byID[l.SKUID]
		if err := order.AddLine(sku.ID, l.Qty, sku.UnitPrice); err != nil {
			return nil, err
		}
	}
	// Order number is filled lazily by the adapter (it knows the
	// per-day counter); the domain stays clock-and-counter-free.
	if err := s.orders.Create(ctx, order); err != nil {
		return nil, err
	}
	return order, nil
}

func (s *WholesaleService) SubmitOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := o.Submit(); err != nil {
		return nil, err
	}
	if err := s.orders.UpdateStatus(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *WholesaleService) ApproveOrder(ctx context.Context, id, by uuid.UUID) (*domain.WholesaleOrder, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := o.Approve(by); err != nil {
		return nil, err
	}
	if err := s.orders.UpdateStatus(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *WholesaleService) RejectOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := o.Reject(); err != nil {
		return nil, err
	}
	if err := s.orders.UpdateStatus(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *WholesaleService) FulfillOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := o.Fulfill(); err != nil {
		return nil, err
	}
	if err := s.orders.UpdateStatus(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *WholesaleService) CancelOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := o.Cancel(); err != nil {
		return nil, err
	}
	if err := s.orders.UpdateStatus(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *WholesaleService) GetOrder(ctx context.Context, id uuid.UUID) (*domain.WholesaleOrder, error) {
	return s.orders.FindByID(ctx, id)
}

func (s *WholesaleService) ListOrders(ctx context.Context, f port.WholesaleOrderListFilter) ([]domain.WholesaleOrder, int, error) {
	return s.orders.List(ctx, f)
}
