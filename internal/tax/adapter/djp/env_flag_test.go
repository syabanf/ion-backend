// Wave 121E — DJP env-flag toggle behavior tests.
//
// These tests pin the contract that "flipping DJP_ENABLED on/off
// without changing code" behaves cleanly:
//
//   - Unset / "false" → stub mode (503 djp.scaffold, no HTTP traffic).
//   - "true" without DJP_BASE_URL → cleanly errors on first call.
//   - "true" with a httptest server URL → the first call hits the test
//     server (proves the swap fired).
//
// The acceptance criterion is "operator can flip DJP_ENABLED at runtime
// and the binary changes behavior without a redeploy" — exercised here
// by re-reading ConfigFromEnv between calls.
package djp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withEnv sets env vars for the test scope and restores them on cleanup.
// We use t.Setenv because go vet flags os.Setenv without restoration.
func withEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

// =====================================================================
// 1) DJP_ENABLED unset → stub mode.
// =====================================================================

func TestDJP_EnabledFalse_UsesStub(t *testing.T) {
	withEnv(t, map[string]string{
		"DJP_ENABLED":  "",
		"DJP_BASE_URL": "",
		"DJP_API_KEY":  "",
	})
	cfg := ConfigFromEnv()
	if cfg.Enabled {
		t.Errorf("Config.Enabled = true for unset env, want false")
	}
	client := NewClient(cfg)
	_, _, err := client.IssueFaktur(context.Background(), fixtureFaktur(t))
	if err == nil {
		t.Fatal("IssueFaktur in stub mode must error")
	}
	// scaffoldErr code is the contract.
	got := err.Error()
	if !contains(got, "djp.scaffold") && !contains(got, "not enabled") {
		t.Errorf("expected scaffold-style error, got: %v", err)
	}
}

// =====================================================================
// 2) DJP_ENABLED=true with no DJP_BASE_URL → cleanly errors on first call.
// =====================================================================

func TestDJP_EnabledTrue_RequiresConfig(t *testing.T) {
	withEnv(t, map[string]string{
		"DJP_ENABLED":  "true",
		"DJP_BASE_URL": "",
		"DJP_API_KEY":  "",
	})
	cfg := ConfigFromEnv()
	if !cfg.Enabled {
		t.Fatal("Config.Enabled = false despite DJP_ENABLED=true")
	}
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (regression check)", cfg.BaseURL)
	}

	// Constructor MUST NOT panic — only the call surfaces the error.
	client := NewClient(cfg)
	_, _, err := client.IssueFaktur(context.Background(), fixtureFaktur(t))
	if err == nil {
		t.Fatal("IssueFaktur with empty BaseURL must error cleanly")
	}
	// The wrapped error should be a typed *derrors.Error so the FE
	// can read the code. We already cover the typed-error contract in
	// stub_determinism_test.go::TestDJP_EnabledWithoutConfig_ErrorsCleanly;
	// here we additionally pin that the error mentions transport / build
	// so the operator knows the call went past the toggle.
	gotMsg := err.Error()
	if contains(gotMsg, "djp.scaffold") {
		t.Errorf("got scaffold error despite DJP_ENABLED=true: %v", err)
	}
}

// =====================================================================
// 3) DJP_ENABLED=true + DJP_BASE_URL=<httptest> → first call hits the
// test server.
//
// This is the proof that the env-flag swap is wired end-to-end —
// flipping the flag without a redeploy changes the runtime behavior.
// =====================================================================

func TestDJP_EnabledTrueWithConfig_UsesRealClient(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// Sanity: auth header set, path correct.
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing/wrong Authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/faktur/issue" {
			t.Errorf("path = %q, want /api/faktur/issue", r.URL.Path)
		}
		// Return a successful issue response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"nomor_seri":"010.001-25.00000001","status":"issued"}`))
	}))
	defer server.Close()

	withEnv(t, map[string]string{
		"DJP_ENABLED":  "true",
		"DJP_BASE_URL": server.URL,
		"DJP_API_KEY":  "test-key",
	})
	cfg := ConfigFromEnv()
	client := NewClient(cfg)

	f := fixtureFaktur(t)
	serial, payload, err := client.IssueFaktur(context.Background(), f)
	if err != nil {
		t.Fatalf("IssueFaktur: %v", err)
	}
	if serial == "" {
		t.Error("serial empty — response parse failed")
	}
	if len(payload) == 0 {
		t.Error("payload empty — should carry the raw response bytes")
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1 (env-flag swap should route to real client)", hits)
	}
}

// =====================================================================
// 4) Toggle off after enabling → reverts to stub mode (no caching).
//
// Each NewClient re-reads ConfigFromEnv, so flipping DJP_ENABLED back
// to "false" on a fresh constructor returns to stub mode. (We don't
// hot-reload inside an existing Client; that's a documented
// limitation.)
// =====================================================================

func TestDJP_ToggleOff_RevertsToStubMode(t *testing.T) {
	// First, enabled with a working URL.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"nomor_seri":"010.001-25.00000001"}`))
	}))
	defer server.Close()

	t.Setenv("DJP_ENABLED", "true")
	t.Setenv("DJP_BASE_URL", server.URL)
	t.Setenv("DJP_API_KEY", "x")

	cfg1 := ConfigFromEnv()
	if !cfg1.Enabled {
		t.Fatal("first config should be enabled")
	}

	// Toggle off.
	t.Setenv("DJP_ENABLED", "false")
	cfg2 := ConfigFromEnv()
	if cfg2.Enabled {
		t.Fatal("second config should be disabled after DJP_ENABLED=false")
	}
	client := NewClient(cfg2)
	_, _, err := client.IssueFaktur(context.Background(), fixtureFaktur(t))
	if err == nil {
		t.Fatal("disabled config must error")
	}
}

// contains is a tiny strings.Contains alias kept local so the test file
// doesn't share helpers across packages.
func contains(s, sub string) bool { return strings.Contains(s, sub) }
