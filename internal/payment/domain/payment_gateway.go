// Package domain holds the payment bounded context's entities and
// value objects.
//
// Rules (same as identity / reseller / enterprise domains):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map
//     them to the right HTTP status without inspecting strings.
//
// Wave 111 scope: PaymentGateway, PaymentIntent (with full state
// machine), PaymentWebhook, Refund, and H2H Statement / Line. Together
// they cover the five Payment Service sub-modules: Architecture,
// Routing, Webhook, H2H Bank, and Refund.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// GatewayKind partitions gateways into routing buckets. The DB
// CHECK enforces the same enum so a typo in this file fails at
// migration time, not at runtime.
type GatewayKind string

const (
	GatewayKindVABank        GatewayKind = "va_bank"
	GatewayKindVAAggregator  GatewayKind = "va_aggregator"
	GatewayKindEWallet       GatewayKind = "ewallet"
	GatewayKindQRIS          GatewayKind = "qris"
	GatewayKindH2HBank       GatewayKind = "h2h_bank"
	GatewayKindCard          GatewayKind = "card"
	GatewayKindCrypto        GatewayKind = "crypto"
)

// PaymentGateway is one registered payment processor. The routing
// service reads (is_active, priority, MatchesAmount) to choose where
// to send a fresh intent.
//
// `Config` is the encrypted credential bundle decoded by the adapter
// at startup. The domain never sees plaintext secrets — `ConfigEncrypted`
// is opaque bytes.
type PaymentGateway struct {
	ID                uuid.UUID
	Code              string
	Name              string
	Kind              GatewayKind
	IsActive          bool
	Priority          int
	SupportedMethods  []string
	MinAmount         *float64
	MaxAmount         *float64
	ConfigEncrypted   []byte
	ConfigKeyVersion  int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewPaymentGateway constructs a gateway registry entry. The code +
// name pair is required; supported methods can be empty (the gateway
// is then considered configurable later).
func NewPaymentGateway(code, name string, kind GatewayKind, priority int) (*PaymentGateway, error) {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" {
		return nil, errors.Validation("gateway.code_required", "code is required")
	}
	if name == "" {
		return nil, errors.Validation("gateway.name_required", "name is required")
	}
	if !kind.Valid() {
		return nil, errors.Validation("gateway.kind_invalid", "kind must be a recognised value")
	}
	now := time.Now().UTC()
	return &PaymentGateway{
		ID:               uuid.New(),
		Code:             code,
		Name:             name,
		Kind:             kind,
		IsActive:         true,
		Priority:         priority,
		SupportedMethods: []string{},
		ConfigKeyVersion: 1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// Valid reports whether the kind matches one of the known buckets.
func (k GatewayKind) Valid() bool {
	switch k {
	case GatewayKindVABank, GatewayKindVAAggregator, GatewayKindEWallet,
		GatewayKindQRIS, GatewayKindH2HBank, GatewayKindCard, GatewayKindCrypto:
		return true
	}
	return false
}

// MatchesAmount reports whether the gateway can accept the given
// amount. Gateways with nil min/max are unconstrained on that side;
// the routing service uses this helper to filter the candidate list
// before applying priority.
//
// The check is inclusive: amount == min or amount == max is accepted.
func (g *PaymentGateway) MatchesAmount(amount float64) bool {
	if g == nil {
		return false
	}
	if g.MinAmount != nil && amount < *g.MinAmount {
		return false
	}
	if g.MaxAmount != nil && amount > *g.MaxAmount {
		return false
	}
	return true
}

// SupportsMethod reports whether the given method string is listed in
// `SupportedMethods`. Empty list is treated as "supports everything"
// because a freshly-onboarded gateway shouldn't be filtered out before
// its method list is configured.
func (g *PaymentGateway) SupportsMethod(method string) bool {
	if g == nil {
		return false
	}
	if len(g.SupportedMethods) == 0 {
		return true
	}
	for _, m := range g.SupportedMethods {
		if m == method {
			return true
		}
	}
	return false
}
