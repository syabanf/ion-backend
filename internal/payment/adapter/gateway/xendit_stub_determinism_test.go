// Wave 121E — Xendit stub-mode determinism tests.
//
// XenditStub is the aggregator-style gateway: CreatePayment issues a
// synthetic VA derived from intent.id; webhooks are HMAC-signed; refunds
// + status echo state. The contract guarantees:
//
//   - Same intent.id → same VA + same external_ref (deterministic
//     replays for local dev).
//   - HMAC verification is constant-time and consistent across calls.
//   - ParseWebhook is total: every code path returns a parseable
//     ParsedWebhook (or a non-panicking error).
//   - No outbound network calls (XenditStub holds no http.Client).
//
// What this DOES NOT validate:
//   - Real Xendit X-Callback-Token verification (Xendit uses a fixed
//     header token, not HMAC — the stub uses HMAC to exercise the
//     constant-time compare path that the production code will reuse).
//   - Webhook re-delivery / out-of-order handling at the gateway side.
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// fixedIntentID gives every test a deterministic intent identifier so
// the synthetic VA is stable across runs.
var fixedIntentID = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

// =====================================================================
// 1) Same input → same output (deterministic CreatePayment).
// =====================================================================

func TestXendit_StubMode_CreatePaymentDeterministic(t *testing.T) {
	stub := NewXenditStub("test-secret")
	in := port.CreatePaymentInput{
		IntentID:      fixedIntentID,
		InvoiceID:     uuid.New(), // varies but unused by stub
		Amount:        500_000,
		Currency:      "IDR",
		Method:        "va_bca",
		CustomerEmail: "alice@example.invalid",
	}

	first, err := stub.CreatePayment(context.Background(), in)
	if err != nil {
		t.Fatalf("first CreatePayment: %v", err)
	}
	second, err := stub.CreatePayment(context.Background(), in)
	if err != nil {
		t.Fatalf("second CreatePayment: %v", err)
	}
	if first.ExternalRef != second.ExternalRef {
		t.Errorf("ExternalRef changed across calls: %q vs %q", first.ExternalRef, second.ExternalRef)
	}
	if first.VANumber != second.VANumber {
		t.Errorf("VANumber changed across calls: %q vs %q", first.VANumber, second.VANumber)
	}
	if first.PaymentURL != second.PaymentURL {
		t.Errorf("PaymentURL changed across calls: %q vs %q", first.PaymentURL, second.PaymentURL)
	}
	// Sanity: VA must start with the stub's 8800 prefix so the
	// downstream matching code can recognise it.
	if got := first.VANumber[:4]; got != "8800" {
		t.Errorf("VA prefix = %q, want %q", got, "8800")
	}
}

// =====================================================================
// 2) Typed-correct response — fields are populated, ExpiresAt is set.
// =====================================================================

func TestXendit_StubMode_ResponseShape(t *testing.T) {
	stub := NewXenditStub("")
	in := port.CreatePaymentInput{
		IntentID: fixedIntentID,
		Amount:   100_000,
		Currency: "IDR",
		Method:   "va_bca",
	}
	out, err := stub.CreatePayment(context.Background(), in)
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if out.ExternalRef == "" {
		t.Error("ExternalRef empty — downstream payment_intent.update would lose the ref")
	}
	if out.VANumber == "" {
		t.Error("VANumber empty — the FE would have nothing to display")
	}
	if out.ExpiresAt == nil {
		t.Error("ExpiresAt nil — the cron's ExpireStaleIntents has no cutoff")
	}
}

// =====================================================================
// 3) Signature verification — HMAC-SHA256 round-trip.
//
// Empty secret = local-dev mode (always-true). With a secret we exercise
// the constant-time compare path that production will reuse when Xendit
// switches to HMAC (today their callback uses a static token, but our
// adapter is forward-compatible).
// =====================================================================

func TestXendit_StubMode_VerifySignatureRoundTrip(t *testing.T) {
	const secret = "test-webhook-secret"
	stub := NewXenditStub(secret)
	body := []byte(`{"id":"xnd_abcdef","status":"PAID","external_id":"INV-1"}`)

	// Sign the body the same way the simulator would.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !stub.VerifySignature(body, sig) {
		t.Fatal("correct signature rejected")
	}
	if stub.VerifySignature(body, "0000") {
		t.Fatal("wrong signature accepted")
	}

	// Empty-secret stub accepts everything (documented dev affordance).
	devStub := NewXenditStub("")
	if !devStub.VerifySignature(body, "any-value") {
		t.Error("empty-secret stub must accept any signature for local dev")
	}
}

// =====================================================================
// 4) ParseWebhook — terminal states map to the right PaymentStatus.
//
// We assert "paid" → Succeeded with paid_at populated, and that
// re-parsing the same payload twice yields the same ParsedWebhook
// (no time.Now() leakage when paid_at is in the body).
// =====================================================================

func TestXendit_StubMode_ParseWebhookIdempotent(t *testing.T) {
	stub := NewXenditStub("")
	payload := []byte(`{"id":"xnd_evt_1","external_id":"INV-2","status":"PAID","paid_at":"2026-05-23T10:30:00Z","event_id":"evt_1"}`)

	a, err := stub.ParseWebhook(payload)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	b, err := stub.ParseWebhook(payload)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if a.ExternalEventID != b.ExternalEventID {
		t.Errorf("ExternalEventID drift: %q vs %q", a.ExternalEventID, b.ExternalEventID)
	}
	if a.NewStatus != b.NewStatus {
		t.Errorf("NewStatus drift: %q vs %q", a.NewStatus, b.NewStatus)
	}
	if a.NewStatus != domain.PaymentStatusSucceeded {
		t.Errorf("paid status = %q, want %q", a.NewStatus, domain.PaymentStatusSucceeded)
	}
	if a.PaidAt == nil || b.PaidAt == nil {
		t.Fatal("PaidAt must be populated when paid_at is present in the body")
	}
	if !a.PaidAt.Equal(*b.PaidAt) {
		t.Errorf("PaidAt drift across calls: %v vs %v", *a.PaidAt, *b.PaidAt)
	}
}
