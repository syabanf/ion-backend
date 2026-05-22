// Activation gateway — Provision-then-Promote a RADIUS account from
// the install-complete hook.
//
// Idempotency: LocalRadiusClient.Provision is itself idempotent on
// customer_id (returns the existing account on second call), and
// PromoteToPermanent is a forward state transition that the DB-level
// state machine refuses to re-apply when the row is already PERMANENT.
// Together the pair survives a NOC re-approval cleanly.
package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/port"
	networkdomain "github.com/ion-core/backend/internal/network/domain"
)

// ProvisioningRadiusClient is the subset of the RADIUS client we need
// for the activation hook.
type ProvisioningRadiusClient interface {
	Provision(ctx context.Context, in networkdomain.ProvisionInput) (*networkdomain.RadiusAccount, error)
	PromoteToPermanent(ctx context.Context, customerID uuid.UUID) (*networkdomain.RadiusAccount, error)
}

// Activator implements port.ActivationGateway by talking to the in-
// process RADIUS client. Round-4 will swap the client for an HTTP
// stub against the deployed network service without changing this type.
type Activator struct {
	client ProvisioningRadiusClient
}

func NewActivator(c ProvisioningRadiusClient) *Activator {
	return &Activator{client: c}
}

var _ port.ActivationGateway = (*Activator)(nil)

// ProvisionAndActivate mints (or finds) the customer's RADIUS account
// and drives it to PERMANENT_ACTIVE. The username we use is the
// customer_number — stable, human-readable, and unique per customer.
// The bandwidth profile id is the product code for now; round-4 will
// look up a per-product profile from a separate table.
func (a *Activator) ProvisionAndActivate(ctx context.Context, in port.ActivationProjection) error {
	pwd, err := randomPassword(16)
	if err != nil {
		return fmt.Errorf("activation: generate password: %w", err)
	}
	username := in.CustomerNumber
	bandwidth := in.ProductCode
	if bandwidth == "" {
		// Customers without a product (legacy / promo paths) still get
		// a RADIUS account, but the bandwidth profile is left as a
		// placeholder string the operator can edit later.
		bandwidth = "default"
	}
	if _, err := a.client.Provision(ctx, networkdomain.ProvisionInput{
		CustomerID:         in.CustomerID,
		Username:           username,
		PasswordPlain:      pwd,
		BandwidthProfileID: bandwidth,
	}); err != nil {
		return fmt.Errorf("activation: provision radius: %w", err)
	}
	if _, err := a.client.PromoteToPermanent(ctx, in.CustomerID); err != nil {
		return fmt.Errorf("activation: promote radius: %w", err)
	}
	return nil
}

// randomPassword returns a hex-encoded random string of `bytes` bytes
// (so the rendered string is `2*bytes` chars). Used as the initial
// PPPoE password — the customer's ONT is pre-configured by the tech,
// so the human never sees this value.
func randomPassword(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
