package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PaymentMethod is a saved customer payment instrument — a tokenised
// card, a stored VA preference, a wallet account, …. The full PAN is
// NEVER stored; only the gateway-issued masked representation lands in
// `MaskedAccount`.
//
// `IsDefault` is informational — the routing service uses it as a
// nudge, not a hard constraint. The customer may still pick a
// different method per-checkout.
type PaymentMethod struct {
	ID            uuid.UUID
	CustomerID    uuid.UUID
	Kind          string
	GatewayID     uuid.UUID
	MaskedAccount string
	ExpiresAt     *time.Time
	IsDefault     bool
	LastUsedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewPaymentMethod constructs a saved-method record. The customer +
// gateway + kind are required; masked_account is optional (e.g. wallet
// methods may not have one).
func NewPaymentMethod(customerID, gatewayID uuid.UUID, kind, masked string) (*PaymentMethod, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("method.customer_required", "customer_id is required")
	}
	if gatewayID == uuid.Nil {
		return nil, errors.Validation("method.gateway_required", "gateway_id is required")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil, errors.Validation("method.kind_required", "kind is required")
	}
	now := time.Now().UTC()
	return &PaymentMethod{
		ID:            uuid.New(),
		CustomerID:    customerID,
		GatewayID:     gatewayID,
		Kind:          kind,
		MaskedAccount: strings.TrimSpace(masked),
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}
