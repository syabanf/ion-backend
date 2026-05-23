// Wave 121E — BCA Host-to-Host stub-mode determinism tests.
//
// BCA H2H is the "bank drops a daily CSV onto SFTP" pattern. The stub:
//
//   - Parses the canned CSV format documented in bca_h2h_stub.go.
//   - Returns "unsupported" on ParseWebhook (H2H banks don't push).
//   - Returns a placeholder VA on CreatePayment because H2H gateways
//     don't issue per-checkout artefacts (the customer transfers to a
//     static corporate account).
//   - Accepts any signature in VerifySignature (the SFTP channel is
//     auth-gated separately).
//
// What this DOES NOT validate:
//   - Real BCA SFTP credential rotation
//   - Real CSV schema drift (BCA can ship MT940 instead of CSV)
//   - Refund-via-outbound-transfer wiring (out of scope until ops
//     books refunds via the bank portal)
package gateway

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/port"
)

// =====================================================================
// 1) ParseH2HStatement is deterministic across runs.
// =====================================================================

func TestBCAH2H_StubMode_ParseStatementDeterministic(t *testing.T) {
	stub := NewBCAH2HStub()
	csv := []byte(`value_date,reference,amount,description
2026-05-23,INV-2026-001,100000,FROM JOHN DOE
2026-05-23,INV-2026-002,250000,FROM ALICE
2026-05-22,INV-2026-003,75000,FROM BOB
`)

	first, err := stub.ParseH2HStatement(csv)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	second, err := stub.ParseH2HStatement(csv)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("line count: first=%d second=%d, want 3 each", len(first), len(second))
	}
	for i := range first {
		if first[i].Amount != second[i].Amount {
			t.Errorf("row %d Amount drift: %v vs %v", i, first[i].Amount, second[i].Amount)
		}
		if first[i].ReferenceText != second[i].ReferenceText {
			t.Errorf("row %d ReferenceText drift: %q vs %q", i, first[i].ReferenceText, second[i].ReferenceText)
		}
		if !first[i].ValueDate.Equal(second[i].ValueDate) {
			t.Errorf("row %d ValueDate drift: %v vs %v", i, first[i].ValueDate, second[i].ValueDate)
		}
	}
	// First row should match INV-2026-001 / 100000 — pin the contract.
	if first[0].ReferenceText != "INV-2026-001" {
		t.Errorf("row 0 ReferenceText = %q, want %q", first[0].ReferenceText, "INV-2026-001")
	}
	if first[0].Amount != 100_000 {
		t.Errorf("row 0 Amount = %v, want 100000", first[0].Amount)
	}
}

// =====================================================================
// 2) CreatePayment returns the placeholder shape (H2H doesn't issue VAs).
// =====================================================================

func TestBCAH2H_StubMode_CreatePaymentReturnsPlaceholder(t *testing.T) {
	stub := NewBCAH2HStub()
	in := port.CreatePaymentInput{
		IntentID:  uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		InvoiceID: uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		Amount:    100_000,
		Currency:  "IDR",
		Method:    "h2h",
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
	if a.VANumber == "" {
		t.Error("VANumber empty — H2H stub should return a placeholder corporate account")
	}
}

// =====================================================================
// 3) ParseWebhook returns the "unsupported" sentinel without panicking.
//
// H2H banks don't push webhooks; calling ParseWebhook on this gateway
// is a misuse. The stub returns an error rather than a nil panic so
// the dispatcher logs + drops cleanly.
// =====================================================================

func TestBCAH2H_StubMode_ParseWebhookUnsupported(t *testing.T) {
	stub := NewBCAH2HStub()
	_, err := stub.ParseWebhook([]byte(`{"any":"payload"}`))
	if err == nil {
		t.Fatal("ParseWebhook on H2H gateway must return an error")
	}
	// The exact message is part of the contract — operators grep for it.
	want := "webhooks are not supported"
	if !contains(err.Error(), want) {
		t.Errorf("error message = %q, must contain %q", err.Error(), want)
	}
}

// =====================================================================
// 4) ParseH2HStatement rejects empty content and bad CSV gracefully.
// =====================================================================

func TestBCAH2H_StubMode_ParseStatementHandlesBadInput(t *testing.T) {
	stub := NewBCAH2HStub()

	if _, err := stub.ParseH2HStatement(nil); err == nil {
		t.Error("empty content must error")
	}
	if _, err := stub.ParseH2HStatement([]byte("")); err == nil {
		t.Error("empty-string content must error")
	}

	// Rows with bad date are silently skipped (the contract says
	// "skipped, logged at caller") — we just need to assert no panic.
	lines, err := stub.ParseH2HStatement([]byte(`value_date,reference,amount
not-a-date,INV-X,100
2026-05-23,INV-Y,not-a-number
2026-05-23,INV-Z,500
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lines) != 1 {
		t.Errorf("len(lines) = %d, want 1 (only INV-Z is valid)", len(lines))
	}
	if len(lines) > 0 && lines[0].ReferenceText != "INV-Z" {
		t.Errorf("survivor ref = %q, want INV-Z", lines[0].ReferenceText)
	}
}

// contains is a tiny strings.Contains shim so this file stays standalone
// (we already pull strings in production code, but not in this file).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
