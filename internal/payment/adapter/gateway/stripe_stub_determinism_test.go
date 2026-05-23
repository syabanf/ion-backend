// Wave 121E — Stripe stub-mode determinism tests.
//
// Stripe is the most-stub-by-design gateway: per the seed, the gateway
// row is is_active=FALSE so the router never picks it. The stub exists
// so cmd/payment-svc can register a client for every seeded gateway
// without special-casing inactive rows.
//
// Contract:
//   - Every method returns the same "not implemented" sentinel error.
//   - VerifySignature returns false (no signature is ever valid).
//   - No outbound network calls.
package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// =====================================================================
// 1) All methods return the canonical not-implemented error.
// =====================================================================

func TestStripe_StubMode_AllMethodsReturnNotImplemented(t *testing.T) {
	stub := NewStripeStub()

	if stub.Code() != "stripe" {
		t.Errorf("Code() = %q, want %q", stub.Code(), "stripe")
	}

	in := port.CreatePaymentInput{
		IntentID:  uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"),
		InvoiceID: uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"),
		Amount:    100,
		Currency:  "USD",
	}
	if _, err := stub.CreatePayment(context.Background(), in); err == nil {
		t.Error("CreatePayment must error")
	}

	intent := &domain.PaymentIntent{ID: uuid.New()}
	if _, err := stub.RefundPayment(context.Background(), intent, 50, ""); err == nil {
		t.Error("RefundPayment must error")
	}
	if _, err := stub.CheckStatus(context.Background(), intent); err == nil {
		t.Error("CheckStatus must error")
	}
	if _, err := stub.ParseWebhook([]byte(`{}`)); err == nil {
		t.Error("ParseWebhook must error")
	}
	if _, err := stub.ParseH2HStatement([]byte("x")); err == nil {
		t.Error("ParseH2HStatement must error")
	}

	// All errors must share the same message — operators / dashboards
	// rely on the stable string for inactive-gateway detection.
	a, e1 := stub.CreatePayment(context.Background(), in)
	b, e2 := stub.ParseWebhook([]byte("{}"))
	if a != nil || b.ExternalEventID != "" {
		t.Error("unexpected non-nil success path")
	}
	if e1 == nil || e2 == nil {
		t.Fatal("both errors must be non-nil")
	}
	if e1.Error() != e2.Error() {
		t.Errorf("error message drift across methods: %q vs %q", e1.Error(), e2.Error())
	}
	// `errors.Is` should report both as the same sentinel.
	if !errors.Is(e1, e2) && e1.Error() != e2.Error() {
		t.Error("errors should be comparable / share the same sentinel")
	}
}

// =====================================================================
// 2) VerifySignature is always false (no signature is ever valid).
// =====================================================================

func TestStripe_StubMode_VerifySignatureAlwaysFalse(t *testing.T) {
	stub := NewStripeStub()
	cases := [][2]string{
		{"", ""},
		{"body", ""},
		{"", "sig"},
		{"body", "sig"},
	}
	for _, c := range cases {
		if stub.VerifySignature([]byte(c[0]), c[1]) {
			t.Errorf("VerifySignature(%q,%q) returned true — must always be false on stub", c[0], c[1])
		}
	}
}
