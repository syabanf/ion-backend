// Package crm adapts the CRM bounded context's order/customer reads to
// the field port.CRMGateway. In-process today; swap to HTTP when the
// CRM service splits to its own deployment.
//
// The Gateway leans on two sources:
//
//   - CRMService: the in-process CRM usecase, for the rich domain reads
//     (orders + customers + products) used by OrderForWO.
//   - pgxpool:    a direct DB handle for the install-complete activation
//     hook, where we need to flip a customer's status and pull the small
//     projection used by RADIUS provisioning. We use the pool there
//     instead of growing the CRM usecase API because the hook is a
//     narrow cross-cut from field — the CRM service doesn't otherwise
//     care about the activation flow.
package crm

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmdomain "github.com/ion-core/backend/internal/crm/domain"
	crmport "github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CRMService is the subset of crm.usecase.Service we need.
type CRMService interface {
	GetOrder(ctx context.Context, id uuid.UUID) (*crmdomain.Order, error)
	GetCustomer(ctx context.Context, id uuid.UUID) (*crmdomain.Customer, error)
	ListProducts(ctx context.Context, f crmport.ProductListFilter) ([]crmdomain.Product, error)
}

type Gateway struct {
	svc  CRMService
	pool *pgxpool.Pool
}

// NewGateway constructs a CRM gateway. The pool is used only by the
// activation hook (SetCustomerActive + ActivationProjectionForOrder);
// callers that never approve a BAST can pass nil and the methods that
// need it will return a clean "not wired" error.
func NewGateway(svc CRMService, pool *pgxpool.Pool) *Gateway {
	return &Gateway{svc: svc, pool: pool}
}

var _ port.CRMGateway = (*Gateway)(nil)

// OrderForWO returns the narrow projection field-svc needs to create a WO.
// We tolerate a missing product (returns blank product fields) — round 1
// only uses product code for the WO header label.
func (g *Gateway) OrderForWO(ctx context.Context, orderID uuid.UUID) (*port.OrderProjection, error) {
	o, err := g.svc.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	c, err := g.svc.GetCustomer(ctx, o.CustomerID)
	if err != nil {
		return nil, err
	}
	proj := &port.OrderProjection{
		OrderID:    o.ID,
		CustomerID: c.ID,
		FullName:   c.FullName,
		Phone:      c.Phone,
		Address:    c.Address,
		BranchID:   c.BranchID,
		// Wave 132 — surface the customer's segment classifier so
		// field-svc can stamp the WO's broadband/enterprise category.
		// Stringified here; field-svc owns the canonical normalization.
		CustomerType: string(c.CustomerType),
	}
	if o.ProductID != nil {
		// Best-effort product enrichment. Round-1 ListProducts has no
		// FindByID exposed through the gateway; we scan the list.
		products, err := g.svc.ListProducts(ctx, crmport.ProductListFilter{})
		if err == nil {
			for _, p := range products {
				if p.ID == *o.ProductID {
					proj.ProductCode = p.Code
					proj.ProductName = p.Name
					proj.SpeedMbps = p.SpeedMbps
					proj.TempActivationWindowHrs = p.TempActivationWindowHrs
					// Wave 84 — propagate the product reference + the
					// service schema slot so field-svc can pin them on
					// the new WO row. Both may stay nil when the
					// product hasn't been linked to a schema yet.
					pid := p.ID
					proj.ProductID = &pid
					if p.ServiceSchemaID != nil {
						sid := *p.ServiceSchemaID
						proj.ServiceSchemaID = &sid
					}
					break
				}
			}
		}
	}
	return proj, nil
}

// ActivationProjectionForOrder pulls customer_number + product code +
// speed in one round-trip — the activation hook needs all three to mint
// a RADIUS account. We go direct-SQL to avoid an N+1 walk through the
// product list.
func (g *Gateway) ActivationProjectionForOrder(ctx context.Context, orderID uuid.UUID) (*port.ActivationProjection, error) {
	if g.pool == nil {
		return nil, derrors.New(derrors.KindInternal, "crm.activation_not_wired",
			"activation projection requires a DB pool")
	}
	row := g.pool.QueryRow(ctx, `
		SELECT o.id, o.product_id, o.customer_id, c.customer_number,
		       COALESCE(p.code, ''), COALESCE(p.speed_mbps, 0)
		  FROM crm.orders o
		  JOIN crm.customers c ON c.id = o.customer_id
		  LEFT JOIN crm.products p ON p.id = o.product_id
		 WHERE o.id = $1
	`, orderID)
	var proj port.ActivationProjection
	if err := row.Scan(&proj.OrderID, &proj.ProductID, &proj.CustomerID,
		&proj.CustomerNumber, &proj.ProductCode, &proj.SpeedMbps); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("crm.order_not_found", "order not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "crm.activation_read",
			"read activation projection", err)
	}
	return &proj, nil
}

// SetCustomerActive flips a customer from any non-terminal status to
// 'active'. We deliberately don't gate on the prior status here — the
// usecase enforces "only on first BAST approve of an install WO" — so
// re-runs (e.g. a rejected then re-approved BAST) are idempotent.
func (g *Gateway) SetCustomerActive(ctx context.Context, customerID uuid.UUID) error {
	if g.pool == nil {
		return derrors.New(derrors.KindInternal, "crm.activation_not_wired",
			"SetCustomerActive requires a DB pool")
	}
	tag, err := g.pool.Exec(ctx, `
		UPDATE crm.customers
		   SET status = 'active', updated_at = NOW()
		 WHERE id = $1
		   AND status <> 'terminated'
	`, customerID)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "crm.set_active",
			"flip customer to active", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the customer doesn't exist or they're already terminated.
		// We surface both as not_found because the field service shouldn't
		// be activating a terminated customer.
		return derrors.NotFound("customer.not_active_eligible",
			"customer is missing or already terminated")
	}
	return nil
}
