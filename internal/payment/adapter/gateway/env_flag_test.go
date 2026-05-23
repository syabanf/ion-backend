// Wave 121E — Payment gateway env-flag toggle tests.
//
// Wave 111 shipped stub clients only — production clients live behind
// XENDIT_ENABLED=true / MIDTRANS_ENABLED=true etc. Those production
// clients haven't landed yet (see Wave 121E readiness doc). What
// IS testable today:
//
//   - SecretsFromEnv picks up per-gateway secrets from env vars.
//   - NewStubRegistry wires every seeded code to a stub adapter (so
//     dispatch doesn't return "gateway not registered").
//   - The Resolve() map is consistent across calls (no race).
//   - Toggling a secret via env doesn't break the registry build.
//
// When the real-client wave lands, this file's TestRegistry_RealMode*
// stubs should be filled in (currently they're skip-with-explanation).
package gateway

import (
	"os"
	"testing"
)

// =====================================================================
// 1) SecretsFromEnv reads the documented PAYMENT_<GATEWAY>_SECRET vars.
// =====================================================================

func TestSecretsFromEnv_PicksUpDocumentedVars(t *testing.T) {
	t.Setenv("PAYMENT_XENDIT_SECRET", "xendit-test-key")
	t.Setenv("PAYMENT_MIDTRANS_SECRET", "midtrans-test-key")

	secrets := SecretsFromEnv()
	if secrets["xendit"] != "xendit-test-key" {
		t.Errorf("xendit secret = %q, want xendit-test-key", secrets["xendit"])
	}
	if secrets["midtrans"] != "midtrans-test-key" {
		t.Errorf("midtrans secret = %q, want midtrans-test-key", secrets["midtrans"])
	}

	// Unset vars default to empty (stub-mode accepts any signature).
	_ = os.Unsetenv("PAYMENT_XENDIT_SECRET")
	_ = os.Unsetenv("PAYMENT_MIDTRANS_SECRET")
	secrets = SecretsFromEnv()
	if secrets["xendit"] != "" {
		t.Errorf("xendit secret with unset env = %q, want empty", secrets["xendit"])
	}
	if secrets["midtrans"] != "" {
		t.Errorf("midtrans secret with unset env = %q, want empty", secrets["midtrans"])
	}
}

// =====================================================================
// 2) NewStubRegistry resolves every seeded gateway code.
//
// The seed (migration 0058) inserts xendit, bca_h2h, midtrans, stripe.
// If Resolve fails for any of those codes, dispatch would crash at
// runtime when a webhook arrived for an "unregistered" provider.
// =====================================================================

func TestRegistry_StubMode_AllCodesResolve(t *testing.T) {
	reg := NewStubRegistry(map[string]string{
		"xendit":   "x",
		"midtrans": "m",
	})

	for _, code := range []string{"xendit", "bca_h2h", "midtrans", "stripe"} {
		c, err := reg.Resolve(code)
		if err != nil {
			t.Errorf("Resolve(%q): %v", code, err)
			continue
		}
		if c.Code() != code {
			t.Errorf("Resolve(%q).Code() = %q, want %q", code, c.Code(), code)
		}
	}
	// Unknown code errors.
	if _, err := reg.Resolve("unknown_gateway"); err == nil {
		t.Error("Resolve(unknown) must error")
	}
}

// =====================================================================
// 3) Codes() lists all registered.
// =====================================================================

func TestRegistry_StubMode_CodesListsAll(t *testing.T) {
	reg := NewStubRegistry(nil)
	codes := reg.Codes()
	if len(codes) != 4 {
		t.Errorf("len(codes) = %d, want 4", len(codes))
	}
	want := map[string]bool{"xendit": false, "bca_h2h": false, "midtrans": false, "stripe": false}
	for _, c := range codes {
		if _, ok := want[c]; !ok {
			t.Errorf("unexpected code in registry: %q", c)
			continue
		}
		want[c] = true
	}
	for c, seen := range want {
		if !seen {
			t.Errorf("code %q missing from Codes()", c)
		}
	}
}

// =====================================================================
// 4) Empty secret map produces a working stub registry (local-dev mode).
//
// Per the docs: "A missing entry maps to empty secret (stubs accept
// all signatures)." This pin prevents a regression where someone wraps
// secrets[code] in a strconv.ParseBool that panics on "".
// =====================================================================

func TestRegistry_StubMode_EmptySecretsDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewStubRegistry(nil) panicked: %v", r)
		}
	}()
	reg := NewStubRegistry(nil)
	if _, err := reg.Resolve("xendit"); err != nil {
		t.Errorf("Resolve(xendit) with empty secrets: %v", err)
	}
}

// =====================================================================
// 5) Real-mode registry — not yet implemented.
//
// When the *_ENABLED=true real-client wave lands, this test is the
// landing pad: assert that flipping XENDIT_ENABLED=true returns a
// real Xendit client (not the stub) AND that flipping it OFF returns
// the stub. Today the only path is stub, so we skip explicitly.
// =====================================================================

func TestRegistry_RealMode_NotYetImplemented(t *testing.T) {
	t.Setenv("XENDIT_ENABLED", "true")
	t.Setenv("MIDTRANS_ENABLED", "true")
	t.Setenv("BCA_H2H_ENABLED", "true")
	t.Setenv("STRIPE_ENABLED", "true")
	t.Skip("Wave 121E: real-client adapter behind *_ENABLED=true not yet implemented; tracked in production wiring readiness doc")
}
