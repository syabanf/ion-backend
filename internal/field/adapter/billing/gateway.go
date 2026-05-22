// Package billing adapts the billing context's usecase to the field
// port.BillingGateway. In-process today; HTTP later.
package billing

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/port"
)

// BillingService is the subset of billing.usecase.Service we need.
type BillingService interface {
	IsOrderOTCPaid(ctx context.Context, orderID uuid.UUID) (bool, error)
	CompleteTerminationByWO(ctx context.Context, woID uuid.UUID) error
}

type Gateway struct {
	svc BillingService
}

func NewGateway(svc BillingService) *Gateway {
	return &Gateway{svc: svc}
}

var _ port.BillingGateway = (*Gateway)(nil)

func (g *Gateway) IsOrderOTCPaid(ctx context.Context, orderID uuid.UUID) (bool, error) {
	return g.svc.IsOrderOTCPaid(ctx, orderID)
}

func (g *Gateway) OnTerminationWOCompleted(ctx context.Context, woID uuid.UUID) error {
	return g.svc.CompleteTerminationByWO(ctx, woID)
}
