// Wave 121E (extended in Wave 128A) — HRIS gateway env-flag toggle tests.
//
// Wave 121E pinned that the env flag was a NO-OP — flipping
// HRIS_GATEWAY_ENABLED=true logged a TODO and fell back to the stub.
//
// Wave 128A closes that finding:
//   - Flag unset / empty / "false"               → StubGateway (caller chooses).
//   - Flag=true + HRIS_GATEWAY_URL/API_KEY set   → NewRESTGateway succeeds; first
//                                                  call hits the configured host.
//   - Flag=true + any required var missing       → NewRESTGateway returns a typed
//                                                  *errors.Error (KindValidation),
//                                                  not a silent stub fallback.
package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// 1) StubGateway construction is env-independent.
//
// The stub constructor takes no env input; flipping HRIS_GATEWAY_ENABLED
// doesn't change anything inside the stub. This pin protects against a
// future refactor that might add env reads to NewStubGateway.
// =====================================================================

func TestHRIS_StubGateway_EnvIndependent(t *testing.T) {
	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	withFlag := NewStubGateway()

	t.Setenv("HRIS_GATEWAY_ENABLED", "false")
	withoutFlag := NewStubGateway()

	ctx := context.Background()
	since := time.Unix(0, 0)

	a, err := withFlag.FetchEmployees(ctx, since)
	if err != nil {
		t.Fatalf("withFlag: %v", err)
	}
	b, err := withoutFlag.FetchEmployees(ctx, since)
	if err != nil {
		t.Fatalf("withoutFlag: %v", err)
	}
	if len(a) != len(b) {
		t.Errorf("employee count differs across env: with=%d without=%d", len(a), len(b))
	}
	for i := range a {
		if a[i].EmployeeNo != b[i].EmployeeNo {
			t.Errorf("row %d EmployeeNo drift across env: %q vs %q", i, a[i].EmployeeNo, b[i].EmployeeNo)
		}
	}
}

// =====================================================================
// 2) EnvFlagSet — parses canonical truthy/falsey values.
// =====================================================================

func TestHRIS_EnvFlagSet_Parses(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"  true  ", true}, // whitespace-trimmed
		{"false", false},
		{"", false},
		{"1", false}, // strict — only "true" counts
		{"yes", false},
	}
	for _, c := range cases {
		t.Setenv("HRIS_GATEWAY_ENABLED", c.in)
		if got := EnvFlagSet(); got != c.want {
			t.Errorf("EnvFlagSet() with %q = %v, want %v", c.in, got, c.want)
		}
	}
}

// =====================================================================
// 3) Flag=true + missing config → typed error, not silent fallback.
//
// This is the load-bearing closure of Wave 121E §6.1: an operator who
// sets HRIS_GATEWAY_ENABLED=true but forgets HRIS_GATEWAY_URL or
// HRIS_GATEWAY_API_KEY MUST get a clear failure, not a stub fallback.
// =====================================================================

func TestHRIS_RealMode_MissingURL_ReturnsTypedError(t *testing.T) {
	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	t.Setenv("HRIS_GATEWAY_URL", "")
	t.Setenv("HRIS_GATEWAY_API_KEY", "some-token")

	cfg := RESTConfigFromEnv()
	gw, err := NewRESTGateway(cfg)
	if err == nil {
		t.Fatal("NewRESTGateway with empty HRIS_GATEWAY_URL must return an error")
	}
	if gw != nil {
		t.Error("gateway should be nil when constructor errors")
	}
	de := derrors.As(err)
	if de == nil {
		t.Fatalf("error not typed *errors.Error: %T %v", err, err)
	}
	if de.Kind != derrors.KindValidation {
		t.Errorf("err.Kind = %q, want %q", de.Kind, derrors.KindValidation)
	}
	if de.Code != "hris.gateway.misconfigured" {
		t.Errorf("err.Code = %q, want %q", de.Code, "hris.gateway.misconfigured")
	}
	if !strings.Contains(de.Message, "HRIS_GATEWAY_URL") {
		t.Errorf("err.Message should name the missing var; got %q", de.Message)
	}
}

func TestHRIS_RealMode_MissingAPIKey_ReturnsTypedError(t *testing.T) {
	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	t.Setenv("HRIS_GATEWAY_URL", "https://hris.example/api")
	t.Setenv("HRIS_GATEWAY_API_KEY", "")

	cfg := RESTConfigFromEnv()
	_, err := NewRESTGateway(cfg)
	if err == nil {
		t.Fatal("NewRESTGateway with empty HRIS_GATEWAY_API_KEY must return an error")
	}
	de := derrors.As(err)
	if de == nil || de.Kind != derrors.KindValidation {
		t.Fatalf("expected KindValidation typed error, got %T %v", err, err)
	}
	if !strings.Contains(de.Message, "HRIS_GATEWAY_API_KEY") {
		t.Errorf("err.Message should name the missing var; got %q", de.Message)
	}
}

func TestHRIS_RealMode_MissingBoth_NamesBoth(t *testing.T) {
	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	t.Setenv("HRIS_GATEWAY_URL", "")
	t.Setenv("HRIS_GATEWAY_API_KEY", "")

	cfg := RESTConfigFromEnv()
	_, err := NewRESTGateway(cfg)
	if err == nil {
		t.Fatal("NewRESTGateway with both required vars empty must error")
	}
	de := derrors.As(err)
	if de == nil {
		t.Fatalf("error not typed: %T %v", err, err)
	}
	if !strings.Contains(de.Message, "HRIS_GATEWAY_URL") || !strings.Contains(de.Message, "HRIS_GATEWAY_API_KEY") {
		t.Errorf("err.Message should name both missing vars; got %q", de.Message)
	}
}

// =====================================================================
// 4) Flag=true + valid config → real client, HTTP calls hit the
// configured server.
// =====================================================================

func TestHRIS_RealMode_HittsConfiguredServer(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing/wrong Authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/employees" {
			t.Errorf("path = %q, want /employees", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"employee_no":"EMP99999","full_name":"Real User","status":"active","kyc_completed":true}]`))
	}))
	defer server.Close()

	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	t.Setenv("HRIS_GATEWAY_URL", server.URL)
	t.Setenv("HRIS_GATEWAY_API_KEY", "test-token")

	cfg := RESTConfigFromEnv()
	gw, err := NewRESTGateway(cfg)
	if err != nil {
		t.Fatalf("NewRESTGateway: %v", err)
	}

	emps, err := gw.FetchEmployees(context.Background(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("FetchEmployees: %v", err)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1 (real client must actually call out)", hits)
	}
	if len(emps) != 1 || emps[0].EmployeeNo != "EMP99999" {
		t.Errorf("FetchEmployees returned %+v, want one EMP99999 row", emps)
	}
}

// =====================================================================
// 5) Flag=true + bad upstream → typed *errors.Error (KindUnavailable),
// not panic, not stub fallback.
// =====================================================================

func TestHRIS_RealMode_Non2xxReturnsTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	t.Setenv("HRIS_GATEWAY_ENABLED", "true")
	t.Setenv("HRIS_GATEWAY_URL", server.URL)
	t.Setenv("HRIS_GATEWAY_API_KEY", "x")

	gw, err := NewRESTGateway(RESTConfigFromEnv())
	if err != nil {
		t.Fatalf("NewRESTGateway: %v", err)
	}
	_, err = gw.FetchEmployees(context.Background(), time.Unix(0, 0))
	if err == nil {
		t.Fatal("expected error on 5xx, got nil")
	}
	de := derrors.As(err)
	if de == nil || de.Kind != derrors.KindUnavailable {
		t.Errorf("expected KindUnavailable typed error, got %T %v", err, err)
	}
}
