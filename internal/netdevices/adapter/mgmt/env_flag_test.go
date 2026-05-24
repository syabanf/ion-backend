// Wave 121E (extended in Wave 128A) — netdevices device-mgmt env-flag tests.
//
// Wave 121E pinned that the flag was a NO-OP — flipping
// DEVICE_MGMT_ENABLED=true logged a Warn and fell back to the stub.
//
// Wave 128A closes that finding:
//   - Flag unset / empty / "false"               → StubClient (caller chooses).
//   - Flag=true + BASE_URL/API_KEY set           → NewHTTPClient succeeds; first
//                                                  call hits the configured host.
//   - Flag=true + any required var missing       → NewHTTPClient returns a typed
//                                                  *errors.Error, not silent fallback.
package mgmt

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// 1) StubClient is env-independent.
// =====================================================================

func TestMgmt_StubClient_EnvIndependent(t *testing.T) {
	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	a := NewStubClient(slog.Default())

	t.Setenv("DEVICE_MGMT_ENABLED", "false")
	b := NewStubClient(slog.Default())

	d := fixtureDevice(t)
	ctx := context.Background()

	if err := a.ScheduleFirmwareUpgrade(ctx, d, "v2.0"); err != nil {
		t.Errorf("a: %v", err)
	}
	if err := b.ScheduleFirmwareUpgrade(ctx, d, "v2.0"); err != nil {
		t.Errorf("b: %v", err)
	}
}

// =====================================================================
// 2) EnvFlagSet — parses canonical truthy/falsey values.
// =====================================================================

func TestMgmt_EnvFlagSet_Parses(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"  true  ", true},
		{"false", false},
		{"", false},
		{"1", false},
		{"yes", false},
	}
	for _, c := range cases {
		t.Setenv("DEVICE_MGMT_ENABLED", c.in)
		if got := EnvFlagSet(); got != c.want {
			t.Errorf("EnvFlagSet() with %q = %v, want %v", c.in, got, c.want)
		}
	}
}

// =====================================================================
// 3) Flag=true + missing config → typed error.
// =====================================================================

func TestMgmt_RealMode_MissingBaseURL_ReturnsTypedError(t *testing.T) {
	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	t.Setenv("DEVICE_MGMT_BASE_URL", "")
	t.Setenv("DEVICE_MGMT_API_KEY", "token")

	cfg := HTTPConfigFromEnv()
	client, err := NewHTTPClient(cfg)
	if err == nil {
		t.Fatal("NewHTTPClient with empty DEVICE_MGMT_BASE_URL must error")
	}
	if client != nil {
		t.Error("client should be nil on error")
	}
	de := derrors.As(err)
	if de == nil || de.Kind != derrors.KindValidation {
		t.Fatalf("expected typed KindValidation error, got %T %v", err, err)
	}
	if de.Code != "netdevices.mgmt.misconfigured" {
		t.Errorf("err.Code = %q, want %q", de.Code, "netdevices.mgmt.misconfigured")
	}
	if !strings.Contains(de.Message, "DEVICE_MGMT_BASE_URL") {
		t.Errorf("err.Message should name missing var; got %q", de.Message)
	}
}

func TestMgmt_RealMode_MissingAPIKey_ReturnsTypedError(t *testing.T) {
	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	t.Setenv("DEVICE_MGMT_BASE_URL", "https://mgmt.example/api")
	t.Setenv("DEVICE_MGMT_API_KEY", "")

	cfg := HTTPConfigFromEnv()
	_, err := NewHTTPClient(cfg)
	if err == nil {
		t.Fatal("NewHTTPClient with empty DEVICE_MGMT_API_KEY must error")
	}
	de := derrors.As(err)
	if de == nil || de.Kind != derrors.KindValidation {
		t.Fatalf("expected typed KindValidation error, got %T %v", err, err)
	}
	if !strings.Contains(de.Message, "DEVICE_MGMT_API_KEY") {
		t.Errorf("err.Message should name missing var; got %q", de.Message)
	}
}

// =====================================================================
// 4) Flag=true + valid config → real client, HTTP calls hit the server.
// =====================================================================

func TestMgmt_RealMode_HittsConfiguredServer(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing/wrong Authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/firmware/schedule" {
			t.Errorf("path = %q, want /firmware/schedule", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	t.Setenv("DEVICE_MGMT_BASE_URL", server.URL)
	t.Setenv("DEVICE_MGMT_API_KEY", "test-token")

	cfg := HTTPConfigFromEnv()
	client, err := NewHTTPClient(cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	d := fixtureDevice(t)
	if err := client.ScheduleFirmwareUpgrade(context.Background(), d, "v2.0"); err != nil {
		t.Fatalf("ScheduleFirmwareUpgrade: %v", err)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1", hits)
	}
}

// =====================================================================
// 5) Flag=true + bad upstream → typed *errors.Error (KindUnavailable).
// =====================================================================

func TestMgmt_RealMode_Non2xxReturnsTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	t.Setenv("DEVICE_MGMT_BASE_URL", server.URL)
	t.Setenv("DEVICE_MGMT_API_KEY", "x")

	client, err := NewHTTPClient(HTTPConfigFromEnv())
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	err = client.ScheduleFirmwareUpgrade(context.Background(), fixtureDevice(t), "v2.0")
	if err == nil {
		t.Fatal("expected error on 5xx, got nil")
	}
	de := derrors.As(err)
	if de == nil || de.Kind != derrors.KindUnavailable {
		t.Errorf("expected KindUnavailable, got %T %v", err, err)
	}
}

// =====================================================================
// 6) Real client with nil device → typed validation error (mirrors the
// stub's tolerance but surfaces it cleanly).
// =====================================================================

func TestMgmt_RealMode_NilDevice_TypedValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("real client must NOT call upstream when device is nil")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("DEVICE_MGMT_ENABLED", "true")
	t.Setenv("DEVICE_MGMT_BASE_URL", server.URL)
	t.Setenv("DEVICE_MGMT_API_KEY", "x")

	client, err := NewHTTPClient(HTTPConfigFromEnv())
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	err = client.ScheduleFirmwareUpgrade(context.Background(), nil, "v2.0")
	if err == nil {
		t.Fatal("expected validation error for nil device")
	}
	de := derrors.As(err)
	if de == nil || de.Kind != derrors.KindValidation {
		t.Errorf("expected KindValidation, got %T %v", err, err)
	}
}
