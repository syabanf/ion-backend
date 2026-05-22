// Package network adapts the RADIUS client to billing.port.NetworkGateway.
// Round-2 calls the in-process network adapter; round-4 can replace
// this with a real ION Radius HTTP call.
package network

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/port"
	networkdomain "github.com/ion-core/backend/internal/network/domain"
)

// RadiusClient is the subset of network.RadiusClient we use.
type RadiusClient interface {
	Suspend(ctx context.Context, customerID uuid.UUID) (*networkdomain.RadiusAccount, error)
	Restore(ctx context.Context, customerID uuid.UUID) (*networkdomain.RadiusAccount, error)
	Deactivate(ctx context.Context, customerID uuid.UUID) (*networkdomain.RadiusAccount, error)
}

type Gateway struct {
	client RadiusClient
}

func New(client RadiusClient) *Gateway {
	return &Gateway{client: client}
}

var _ port.NetworkGateway = (*Gateway)(nil)

// SuspendCustomer flips the customer's RADIUS account to SUSPENDED.
// Errors are non-fatal in the scheduler — we still mark the customer
// suspended in CRM; the operator can manually re-sync RADIUS later.
// We surface the error here so the caller can log it.
func (g *Gateway) SuspendCustomer(ctx context.Context, customerID uuid.UUID, reason string) error {
	_, err := g.client.Suspend(ctx, customerID)
	return err
}

func (g *Gateway) RestoreCustomer(ctx context.Context, customerID uuid.UUID) error {
	_, err := g.client.Restore(ctx, customerID)
	return err
}

// DeactivateCustomer is the terminal RADIUS state, called after the
// device-retrieval BAST is approved on a termination WO.
func (g *Gateway) DeactivateCustomer(ctx context.Context, customerID uuid.UUID, _ string) error {
	_, err := g.client.Deactivate(ctx, customerID)
	return err
}
