// Wave 121E — Midtrans stub-mode determinism tests.
//
// MidtransStub mirrors XenditStub but with a SHA-512-shaped signing
// scheme in production. For the stub we use HMAC-SHA256 to exercise
// the constant-time compare path.
//
// What this DOES NOT validate:
//   - Real Midtrans SHA-512 signature key derivation
//   - Real settlement-time timezone handling
//   - Fraud-status interpretation (deny vs. challenge)
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

var midtransFixedIntentID = uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")

// =====================================================================
// 1) CreatePayment is deterministic and produces the right prefix.
// =====================================================================

func TestMidtrans_StubMode_CreatePaymentDeterministic(t *testing.T) {
	stub := NewMidtransStub("test-secret")
	in := port.CreatePaymentInput{
		IntentID: midtransFixedIntentID,
		Amount:   250_000,
		Currency: "IDR",
		Method:   "va_bni",
	}
	a, err := stub.CreatePayment(context.Background(), in)
	if err != nil {
		t.Fatalf("first CreatePayment: %v", err)
	}
	b, err := stub.CreatePayment(context.Background(), in)
	if err != nil {
		t.Fatalf("second CreatePayment: %v", err)
	}
	if a.ExternalRef != b.ExternalRef {
		t.Errorf("ExternalRef drift: %q vs %q", a.ExternalRef, b.ExternalRef)
	}
	if a.VANumber != b.VANumber {
		t.Errorf("VANumber drift: %q vs %q", a.VANumber, b.VANumber)
	}
	if !strings.HasPrefix(a.ExternalRef, "mt_") {
		t.Errorf("ExternalRef = %q, must start with mt_", a.ExternalRef)
	}
	if !strings.HasPrefix(a.VANumber, "9900") {
		t.Errorf("VANumber = %q, must start with 9900 (Midtrans stub prefix)", a.VANumber)
	}
}

// =====================================================================
// 2) Signature verification HMAC round-trip.
// =====================================================================

func TestMidtrans_StubMode_VerifySignatureRoundTrip(t *testing.T) {
	const secret = "midtrans-test-key"
	stub := NewMidtransStub(secret)
	body := []byte(`{"transaction_id":"mt_evt_1","order_id":"INV-3","transaction_status":"settlement"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !stub.VerifySignature(body, sig) {
		t.Fatal("correct signature rejected")
	}
	if stub.VerifySignature(body, "deadbeef") {
		t.Fatal("wrong signature accepted")
	}
}

// =====================================================================
// 3) ParseWebhook — settlement maps to Succeeded; idempotent.
// =====================================================================

func TestMidtrans_StubMode_ParseWebhookIdempotent(t *testing.T) {
	stub := NewMidtransStub("")
	body := []byte(`{"transaction_id":"mt_evt_2","order_id":"INV-4","transaction_status":"settlement","settlement_time":"2026-05-23 10:30:00","status_code":"200"}`)

	a, err := stub.ParseWebhook(body)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	b, err := stub.ParseWebhook(body)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if a.NewStatus != b.NewStatus {
		t.Errorf("NewStatus drift: %q vs %q", a.NewStatus, b.NewStatus)
	}
	if a.NewStatus != domain.PaymentStatusSucceeded {
		t.Errorf("settlement should map to Succeeded, got %q", a.NewStatus)
	}
	if a.ExternalEventID != "mt_evt_2" {
		t.Errorf("ExternalEventID = %q, want mt_evt_2", a.ExternalEventID)
	}
	if a.PaidAt == nil || b.PaidAt == nil {
		t.Fatal("PaidAt must be set when settlement_time present")
	}
	if !a.PaidAt.Equal(*b.PaidAt) {
		t.Errorf("PaidAt drift: %v vs %v", a.PaidAt, b.PaidAt)
	}

	// Failure path — deny status maps to Failed.
	denyBody := []byte(`{"transaction_id":"mt_evt_3","order_id":"INV-5","transaction_status":"deny","status_code":"202","status_message":"insufficient_funds"}`)
	c, err := stub.ParseWebhook(denyBody)
	if err != nil {
		t.Fatalf("deny parse: %v", err)
	}
	if c.NewStatus != domain.PaymentStatusFailed {
		t.Errorf("deny should map to Failed, got %q", c.NewStatus)
	}
	if c.FailureCode == "" {
		t.Error("FailureCode should be populated for deny")
	}
}
