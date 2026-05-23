package gateway

import (
	"context"
	"errors"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// StripeStub returns NotImplemented for everything. Stripe is seeded
// in the gateway registry as `is_active=FALSE` so the routing service
// won't pick it until a future wave wires the real client; the stub
// exists so the wire-up code in main.go can register a client for
// every seeded gateway without special-casing inactive rows.
type StripeStub struct{}

func NewStripeStub() *StripeStub { return &StripeStub{} }

var _ port.GatewayClient = (*StripeStub)(nil)

func (s *StripeStub) Code() string { return "stripe" }

var errStripeStub = errors.New("stripe: client not implemented (gateway is inactive until international expansion lands)")

func (s *StripeStub) CreatePayment(ctx context.Context, in port.CreatePaymentInput) (*port.CreatePaymentResult, error) {
	return nil, errStripeStub
}

func (s *StripeStub) RefundPayment(ctx context.Context, intent *domain.PaymentIntent, amount float64, reason string) (*port.RefundResult, error) {
	return nil, errStripeStub
}

func (s *StripeStub) CheckStatus(ctx context.Context, intent *domain.PaymentIntent) (*port.CheckStatusResult, error) {
	return nil, errStripeStub
}

func (s *StripeStub) VerifySignature(payload []byte, signature string) bool { return false }

func (s *StripeStub) ParseWebhook(payload []byte) (port.ParsedWebhook, error) {
	return port.ParsedWebhook{}, errStripeStub
}

func (s *StripeStub) ParseH2HStatement(content []byte) ([]port.ParsedH2HLine, error) {
	return nil, errStripeStub
}
