// Package radius — FreeRadiusClient is the protocol-bridge adapter
// (Wave 80 phase 2). This file is the **interface stub** that lands
// in phase 1: the struct + methods exist so callers can compile
// against the contract, but every protocol-emitting method currently
// returns derrors.NotImplemented.
//
// Phase 2 (separate session) will:
//   - Add the `layeh.com/radius` go.mod dep
//   - Implement RFC 2865 (provisioning) + RFC 5176 (CoA / Disconnect)
//   - Stand up a mock RADIUS server for integration tests
//   - Wire ReadSealedPassword() through to a pkg/cryptutil.Sealer
//     consuming the password_sealed column added in migration 0054
//
// Today the in-process LocalRadiusClient is the canonical impl; the
// service wiring picks one or the other via the new env var
// RADIUS_PROVIDER (=local|freeradius). When unset or "local" the
// service stays on LocalRadiusClient.
package radius

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	"github.com/ion-core/backend/pkg/cryptutil"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// FreeRadiusConfig is the operator-tunable surface. None of the fields
// are sensitive on their own; the shared secret is sealed and the
// endpoint is internal-only.
type FreeRadiusConfig struct {
	// Endpoint is the radsec / RADIUS authority, e.g.
	// "radsec://radius.ion.local:2083". Phase 2 dials this.
	Endpoint string
	// SharedKey is the FreeRADIUS shared secret used for RFC 2865
	// message-authenticator + RFC 5176 CoA packets. Stored sealed at
	// rest; the runtime opens it once at startup via the sealer.
	SharedKey []byte
}

// FreeRadiusClient implements port.RadiusClient by speaking RADIUS to
// an external server. Phase-1 status: every protocol method returns
// "not implemented"; the struct exists so the wiring contract stays
// stable while phase 2 fills it in.
type FreeRadiusClient struct {
	cfg    FreeRadiusConfig
	sealer *cryptutil.Sealer
	log    *slog.Logger
}

func NewFreeRadiusClient(cfg FreeRadiusConfig, sealer *cryptutil.Sealer, log *slog.Logger) *FreeRadiusClient {
	return &FreeRadiusClient{
		cfg:    cfg,
		sealer: sealer,
		log:    log.With("component", "radius_freeradius"),
	}
}

var _ port.RadiusClient = (*FreeRadiusClient)(nil)

// notImplemented produces a uniform "phase 2" error so handlers can
// surface a clear message instead of an opaque internal failure.
func notImplemented(op string) error {
	return derrors.New(derrors.KindInternal,
		"radius.freeradius_not_implemented",
		"FreeRADIUS protocol bridge is in Wave 80 phase 2 — current build only ships the interface stub; deploy with RADIUS_PROVIDER=local until phase 2 lands ("+op+")")
}

func (c *FreeRadiusClient) Provision(_ context.Context, _ domain.ProvisionInput) (*domain.RadiusAccount, error) {
	return nil, notImplemented("Provision")
}
func (c *FreeRadiusClient) PromoteToPermanent(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, notImplemented("PromoteToPermanent")
}
func (c *FreeRadiusClient) Suspend(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, notImplemented("Suspend")
}
func (c *FreeRadiusClient) Restore(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, notImplemented("Restore")
}
func (c *FreeRadiusClient) Deactivate(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, notImplemented("Deactivate")
}
func (c *FreeRadiusClient) Find(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, notImplemented("Find")
}
