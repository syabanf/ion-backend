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

// ProvisionAtWO mints the customer's RADIUS account in TEMPORARY state
// with a `temp_expires_at = now() + windowHours`. Per PRD §13 this fires
// from CreateWOFromOrder so the account exists from the moment a
// technician picks up the WO. Idempotent: if the row already exists in
// TEMPORARY, the deadline is refreshed; in PERMANENT/SUSPENDED it's
// left untouched.
//
// windowHours = 0 falls back to the system default (72h) so callers
// missing product context still produce a row.
func (a *Activator) ProvisionAtWO(ctx context.Context, in port.ActivationProjection, windowHours int) error {
	pwd, err := randomPassword(16)
	if err != nil {
		return fmt.Errorf("activation: generate password: %w", err)
	}
	username := in.CustomerNumber
	bandwidth := in.ProductCode
	if bandwidth == "" {
		bandwidth = "default"
	}
	if _, err := a.client.Provision(ctx, networkdomain.ProvisionInput{
		CustomerID:         in.CustomerID,
		Username:           username,
		PasswordPlain:      pwd,
		BandwidthProfileID: bandwidth,
		WindowHours:        windowHours,
	}); err != nil {
		return fmt.Errorf("activation: provision radius: %w", err)
	}
	return nil
}

// ProvisionAndActivate mints (or finds) the customer's RADIUS account
// and drives it to PERMANENT_ACTIVE. The username we use is the
// customer_number — stable, human-readable, and unique per customer.
// The bandwidth profile id is the product code for now; round-4 will
// look up a per-product profile from a separate table.
//
// As of Wave 65 this method preserves its original contract — it both
// provisions and promotes — but for the happy path the row was already
// minted at WO creation by ProvisionAtWO, so the Provision call here
// just refreshes the expiry then PromoteToPermanent flips state.
func (a *Activator) ProvisionAndActivate(ctx context.Context, in port.ActivationProjection) error {
	if err := a.ProvisionAtWO(ctx, in, 0); err != nil {
		return err
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
