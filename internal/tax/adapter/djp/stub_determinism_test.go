// Wave 121E — DJP e-Faktur stub-mode determinism tests.
//
// These tests pin the contract that the stub gateway honours today:
//
//   - Same input → same typed-correct error (deterministic).
//   - No outbound network traffic (the stub holds no HTTP client; we
//     re-assert this by checking the type doesn't expose one and that
//     a non-routable BaseURL in Client form is never dialled in stub
//     mode).
//   - Idempotent CheckStatus — N calls with the same nomor_seri return
//     the same shaped error.
//   - DJP_ENABLED=true without DJP_BASE_URL must surface a clean,
//     actionable transport error rather than panic or silent fallback.
//
// What this DOES NOT validate (see Wave 121E readiness doc):
//   - Real DJP API timeout / 5xx behaviour
//   - Real signature crypto (DJP cert chain)
//   - Schema drift between the stub's issueResponse and prod payload
package djp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/tax/domain"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// fixtureFaktur builds a deterministic Draft faktur for the stub tests.
// All ids are fixed so consecutive runs produce the same byte-for-byte
// inputs to the gateway.
func fixtureFaktur(t *testing.T) *domain.FakturPajak {
	t.Helper()
	invoiceID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	subsidiaryID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	f, err := domain.NewDraftFaktur(
		invoiceID,
		subsidiaryID,
		domain.JenisFakturStandard,
		"01.234.567.8-901.000",
		1_000_000,
		110_000,
	)
	if err != nil {
		t.Fatalf("fixtureFaktur: %v", err)
	}
	return f
}

// =====================================================================
// 1) Stub returns the documented scaffold error.
// =====================================================================

func TestDJP_StubMode_ReturnsScaffoldError(t *testing.T) {
	gw := NewStubGateway()
	f := fixtureFaktur(t)

	serial, payload, err := gw.IssueFaktur(context.Background(), f)
	if err == nil {
		t.Fatal("StubGateway.IssueFaktur must return an error")
	}
	if serial != "" {
		t.Errorf("serial = %q, want empty", serial)
	}
	if payload != nil {
		t.Errorf("payload should be nil in stub mode, got %d bytes", len(payload))
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type = %T, want *derrors.Error", err)
	}
	if de.Kind != derrors.KindUnavailable {
		t.Errorf("kind = %q, want %q", de.Kind, derrors.KindUnavailable)
	}
	if de.Code != "djp.scaffold" {
		t.Errorf("code = %q, want %q", de.Code, "djp.scaffold")
	}
}

// =====================================================================
// 2) No outbound network: the StubGateway has zero fields so it can't
// dial. We also wire a Client in stub mode against a tripwire server
// and assert nobody hit it.
// =====================================================================

func TestDJP_StubMode_NoNetwork(t *testing.T) {
	// Tripwire HTTP server — any dial in stub mode fails the test.
	tripped := false
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tripped = true
		t.Errorf("stub-mode DJP gateway dialled %s %s — must not make real network calls", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tripwire.Close()

	cfg := Config{
		Enabled: false, // stub mode
		BaseURL: tripwire.URL,
		APIKey:  "unused-in-stub-mode",
		Timeout: 2 * time.Second,
	}
	client := NewClient(cfg)
	f := fixtureFaktur(t)

	_, _, err := client.IssueFaktur(context.Background(), f)
	if err == nil {
		t.Fatal("client.IssueFaktur in stub mode must error (scaffold)")
	}
	if tripped {
		t.Fatal("tripwire fired: stub mode hit the network")
	}

	_, _, err = client.CheckStatus(context.Background(), "010.001-25.00000001")
	if err == nil {
		t.Fatal("client.CheckStatus in stub mode must error (scaffold)")
	}
	if tripped {
		t.Fatal("tripwire fired on CheckStatus: stub mode hit the network")
	}
}

// =====================================================================
// 3) EnabledWithoutConfig — flipping the flag without DJP_BASE_URL must
// produce a transport-level error rather than panic.
// =====================================================================

func TestDJP_EnabledWithoutConfig_ErrorsCleanly(t *testing.T) {
	// Enabled but BaseURL empty → the real HTTP client tries to
	// dial "" which surfaces a wrapped transport error rather than
	// a panic or silent fall-back to stub.
	cfg := Config{
		Enabled: true,
		BaseURL: "", // missing — operator error
		APIKey:  "",
		Timeout: 500 * time.Millisecond,
	}
	client := NewClient(cfg)

	f := fixtureFaktur(t)
	_, _, err := client.IssueFaktur(context.Background(), f)
	if err == nil {
		t.Fatal("IssueFaktur with DJP_ENABLED=true + empty BASE_URL must error")
	}
	// Must be a typed *derrors.Error so the HTTP adapter maps it to
	// a 503 with code djp.transport (or djp.build_request) — not a
	// raw panic / driver leak.
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type = %T, want *derrors.Error (got %v)", err, err)
	}
	if de.Code == "" {
		t.Error("typed error must have a stable Code so the FE can react")
	}
	// We don't pin the exact code because both djp.transport (bad URL)
	// and djp.build_request (invalid URL parse) are acceptable —
	// the load-bearing assertion is "actionable + typed".
}

// =====================================================================
// 4) Idempotency — calling CheckStatus 3x must yield the same error.
// =====================================================================

func TestDJP_StubMode_Idempotent(t *testing.T) {
	gw := NewStubGateway()
	const nomorSeri = "010.001-25.00000001"

	var prev string
	for i := 0; i < 3; i++ {
		status, payload, err := gw.CheckStatus(context.Background(), nomorSeri)
		if err == nil {
			t.Fatalf("call %d: CheckStatus must error in stub mode", i)
		}
		if status != "" {
			t.Errorf("call %d: status = %q, want empty", i, status)
		}
		if payload != nil {
			t.Errorf("call %d: payload non-nil", i)
		}
		var de *derrors.Error
		if !errors.As(err, &de) {
			t.Fatalf("call %d: err type = %T", i, err)
		}
		// Determinism: the rendered error string must match prior calls.
		if i > 0 && de.Error() != prev {
			t.Errorf("call %d: error string changed across calls — stub is non-deterministic\n  prev: %s\n  now:  %s", i, prev, de.Error())
		}
		prev = de.Error()
	}
}
