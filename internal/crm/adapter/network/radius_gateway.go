// Wave 83 — adapts CRM's RadiusGateway port to network's RadiusClient.
//
// CRM HTTP flows (addon buy/sell, plan change apply) call this on a
// mutation that should re-push the RADIUS profile. The adapter calls
// `Restore` on the network client — for the DB-stub that's a no-op
// status flip (and an audit row from Wave 81b); for the real
// FreeRADIUS adapter (Wave 80) that's a CoA packet carrying the new
// bandwidth profile.
//
// We deliberately swallow "no RADIUS account exists" as a non-error.
// A customer might buy an addon during pending-install (no RADIUS row
// yet); on activation the BAST verify path will provision the account
// with the addon already in place, so dropping the refresh here is
// safe.
package network

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/port"
	networkport "github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type RadiusGateway struct {
	client networkport.RadiusClient
}

func NewRadiusGateway(client networkport.RadiusClient) *RadiusGateway {
	return &RadiusGateway{client: client}
}

var _ port.RadiusGateway = (*RadiusGateway)(nil)

// RefreshForCustomer pushes the post-mutation profile to RADIUS. The
// reason tag travels into the network adapter's audit log via the
// transition path; CRM-side audit is the responsibility of the caller
// (it already records the addon / plan_change row).
func (g *RadiusGateway) RefreshForCustomer(
	ctx context.Context, customerID uuid.UUID, reason string,
) error {
	if g.client == nil {
		return nil
	}
	// Find first — Restore on a missing account is a NotFound. Pre-
	// activation addons / plan changes shouldn't surface that error
	// up the HTTP stack because the user's request did land in CRM.
	acct, err := g.client.Find(ctx, customerID)
	if err != nil {
		// `Find` returns NotFound when the account hasn't been
		// provisioned yet (typical for pending-install customers).
		// We treat that as "nothing to refresh" — see package doc.
		if derrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if acct == nil {
		return nil
	}
	// Restore pushes the customer back to PERMANENT_ACTIVE with the
	// current bandwidth profile. The audit row emitted from the
	// LocalRadiusClient.transition path carries reason via the
	// surrounding audit context; we don't need to write a separate
	// row from this gateway. reason is currently unused at the wire
	// level but kept for log readability.
	_ = reason
	if _, err := g.client.Restore(ctx, customerID); err != nil {
		return err
	}
	return nil
}
