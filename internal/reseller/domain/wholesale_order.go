package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// WholesaleOrderStatus mirrors the DB CHECK enum.
//
// Lifecycle (each transition has its own method below):
//
//	draft → submitted → approved → fulfilled
//	                ↘ rejected   (terminal)
//	                ↘ cancelled  (terminal; also from draft)
type WholesaleOrderStatus string

const (
	WholesaleOrderStatusDraft     WholesaleOrderStatus = "draft"
	WholesaleOrderStatusSubmitted WholesaleOrderStatus = "submitted"
	WholesaleOrderStatusApproved  WholesaleOrderStatus = "approved"
	WholesaleOrderStatusRejected  WholesaleOrderStatus = "rejected"
	WholesaleOrderStatusFulfilled WholesaleOrderStatus = "fulfilled"
	WholesaleOrderStatusCancelled WholesaleOrderStatus = "cancelled"
)

// WholesaleOrder is the order header. The reseller tenant is the
// authoritative scoping key — every read on the platform surface
// MUST filter by ResellerAccountID so a missing WHERE clause becomes
// a 404 rather than a leak.
type WholesaleOrder struct {
	ID                   uuid.UUID
	ResellerAccountID    uuid.UUID
	SupplierSubsidiaryID uuid.UUID
	OrderNo              string
	Status               WholesaleOrderStatus
	Subtotal             float64
	Total                float64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ApprovedAt           *time.Time
	FulfilledAt          *time.Time

	// Lines populated by the repo on FindByID; create paths build the
	// slice on the order before handing to the repo's tx-aware Create.
	Lines []WholesaleOrderLine

	// ApprovedBy snapshots the admin actor for audit. Pointer so a
	// freshly-created order serializes as JSON null.
	ApprovedBy *uuid.UUID
}

// WholesaleOrderLine is one item in an order. UnitPrice is snapshotted
// at line creation so later catalog price changes don't retro-edit
// already-submitted orders. LineTotal = qty * unit_price; the usecase
// recomputes it in the same tx as the insert.
type WholesaleOrderLine struct {
	ID        uuid.UUID
	OrderID   uuid.UUID
	SKUID     uuid.UUID
	Qty       int
	UnitPrice float64
	LineTotal float64
}

// NewWholesaleOrder constructs a draft order. The supplier id is
// captured on the header so list views can filter by supplier without
// joining lines. Lines are appended via AddLine before the repo's
// Create call.
func NewWholesaleOrder(resellerID, supplierID uuid.UUID) (*WholesaleOrder, error) {
	if resellerID == uuid.Nil {
		return nil, errors.Validation("order.reseller_required", "reseller_account_id is required")
	}
	if supplierID == uuid.Nil {
		return nil, errors.Validation("order.supplier_required", "supplier_subsidiary_id is required")
	}
	now := time.Now().UTC()
	return &WholesaleOrder{
		ID:                   uuid.New(),
		ResellerAccountID:    resellerID,
		SupplierSubsidiaryID: supplierID,
		Status:               WholesaleOrderStatusDraft,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

// AddLine appends a validated line and updates the snapshot totals.
// SKUs themselves are looked up in the usecase (catalog repo) before
// this call so the domain only deals with already-validated values.
func (o *WholesaleOrder) AddLine(skuID uuid.UUID, qty int, unitPrice float64) error {
	if o.Status != WholesaleOrderStatusDraft {
		return errors.Conflict(
			"order.lines_locked",
			"lines can only be edited while the order is draft",
		)
	}
	if skuID == uuid.Nil {
		return errors.Validation("order.line_sku_required", "sku_id is required")
	}
	if qty <= 0 {
		return errors.Validation("order.line_qty_invalid", "qty must be > 0")
	}
	if unitPrice < 0 {
		return errors.Validation("order.line_price_invalid", "unit_price must be >= 0")
	}
	line := WholesaleOrderLine{
		ID:        uuid.New(),
		OrderID:   o.ID,
		SKUID:     skuID,
		Qty:       qty,
		UnitPrice: unitPrice,
		LineTotal: float64(qty) * unitPrice,
	}
	o.Lines = append(o.Lines, line)
	o.recomputeTotals()
	return nil
}

func (o *WholesaleOrder) recomputeTotals() {
	var sub float64
	for _, l := range o.Lines {
		sub += l.LineTotal
	}
	o.Subtotal = sub
	// Total mirrors subtotal for now; tax / shipping land in a later
	// wave when the wholesale invoicing surface is wired up.
	o.Total = sub
	o.UpdatedAt = time.Now().UTC()
}

// Submit moves draft → submitted. Requires at least one line so the
// "empty order submitted by accident" failure mode is impossible.
func (o *WholesaleOrder) Submit() error {
	if o.Status != WholesaleOrderStatusDraft {
		return errors.Conflict("order.cannot_submit", "only draft orders can be submitted")
	}
	if len(o.Lines) == 0 {
		return errors.Validation("order.empty", "order must have at least one line")
	}
	o.Status = WholesaleOrderStatusSubmitted
	o.UpdatedAt = time.Now().UTC()
	return nil
}

// Approve moves submitted → approved and snapshots the approver. The
// supplier-side fulfillment step picks up approved orders.
func (o *WholesaleOrder) Approve(by uuid.UUID) error {
	if o.Status != WholesaleOrderStatusSubmitted {
		return errors.Conflict("order.cannot_approve", "only submitted orders can be approved")
	}
	now := time.Now().UTC()
	o.Status = WholesaleOrderStatusApproved
	o.ApprovedAt = &now
	o.ApprovedBy = &by
	o.UpdatedAt = now
	return nil
}

// Reject is terminal — once rejected, the reseller has to file a new
// order rather than mutate this one.
func (o *WholesaleOrder) Reject() error {
	if o.Status != WholesaleOrderStatusSubmitted {
		return errors.Conflict("order.cannot_reject", "only submitted orders can be rejected")
	}
	o.Status = WholesaleOrderStatusRejected
	o.UpdatedAt = time.Now().UTC()
	return nil
}

// Fulfill moves approved → fulfilled. The supplier-side hook (Wave 95+)
// will call this once the underlying provisioning / shipment lands.
func (o *WholesaleOrder) Fulfill() error {
	if o.Status != WholesaleOrderStatusApproved {
		return errors.Conflict("order.cannot_fulfill", "only approved orders can be fulfilled")
	}
	now := time.Now().UTC()
	o.Status = WholesaleOrderStatusFulfilled
	o.FulfilledAt = &now
	o.UpdatedAt = now
	return nil
}

// Cancel is allowed from draft or submitted only — once approved the
// supplier may have started fulfillment, so cancellation needs a
// formal back-out path (out of scope for this wave).
func (o *WholesaleOrder) Cancel() error {
	switch o.Status {
	case WholesaleOrderStatusDraft, WholesaleOrderStatusSubmitted:
		o.Status = WholesaleOrderStatusCancelled
		o.UpdatedAt = time.Now().UTC()
		return nil
	default:
		return errors.Conflict(
			"order.cannot_cancel",
			"only draft or submitted orders can be cancelled",
		)
	}
}
