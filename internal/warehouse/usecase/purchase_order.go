// Wave 85 (Tier 3 starter) — Purchase order use cases.
//
// Surface: create draft, get detail, list, submit, cancel. Approve +
// receive land in Wave 86 alongside the goods-receipt workflow.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithPurchaseOrders wires the PO repo. Optional — the surface
// returns errPONotConfigured cleanly when nil so deployments that
// don't need it stay simple.
func (s *Service) WithPurchaseOrders(r port.PurchaseOrderRepository) *Service {
	s.purchaseOrders = r
	return s
}

func errPONotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "po.not_configured",
		"purchase orders are not configured for this service", nil)
}

// CreatePurchaseOrder constructs a draft PO + lines and persists
// them in one tx. PPN defaults to 11% when zero.
func (s *Service) CreatePurchaseOrder(
	ctx context.Context, in port.CreatePurchaseOrderInput,
) (*port.PurchaseOrderDetail, error) {
	if s.purchaseOrders == nil {
		return nil, errPONotConfigured()
	}
	now := time.Now().UTC()
	po, lines, err := domain.NewPurchaseOrder(
		in.SupplierID, in.BranchID, in.ReceivingWarehouseID,
		in.Lines, in.PPNRate, in.Notes, in.CreatedBy, now,
	)
	if err != nil {
		return nil, err
	}
	po.ExpectedAt = in.ExpectedAt
	if err := s.purchaseOrders.Create(ctx, po, lines); err != nil {
		return nil, err
	}
	return &port.PurchaseOrderDetail{PO: *po, Lines: lines}, nil
}

func (s *Service) GetPurchaseOrder(
	ctx context.Context, id uuid.UUID,
) (*port.PurchaseOrderDetail, error) {
	if s.purchaseOrders == nil {
		return nil, errPONotConfigured()
	}
	return s.purchaseOrders.FindByID(ctx, id)
}

func (s *Service) ListPurchaseOrders(
	ctx context.Context, f port.PurchaseOrderListFilter,
) ([]domain.PurchaseOrder, int, error) {
	if s.purchaseOrders == nil {
		return nil, 0, errPONotConfigured()
	}
	return s.purchaseOrders.List(ctx, f)
}

// SubmitPurchaseOrder advances draft → submitted. The actor is
// stamped on the row for the audit trail.
func (s *Service) SubmitPurchaseOrder(
	ctx context.Context, id, by uuid.UUID,
) (*port.PurchaseOrderDetail, error) {
	if s.purchaseOrders == nil {
		return nil, errPONotConfigured()
	}
	detail, err := s.purchaseOrders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := detail.PO.Submit(by, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.purchaseOrders.UpdateStatus(ctx, id, &detail.PO); err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Service) CancelPurchaseOrder(
	ctx context.Context, id, by uuid.UUID, reason string,
) (*port.PurchaseOrderDetail, error) {
	if s.purchaseOrders == nil {
		return nil, errPONotConfigured()
	}
	detail, err := s.purchaseOrders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := detail.PO.Cancel(by, reason, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.purchaseOrders.UpdateStatus(ctx, id, &detail.PO); err != nil {
		return nil, err
	}
	return detail, nil
}
