package webhookx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeProvider lets us exercise the Middleware deterministically: it
// signs by reversing the body so we can assert both success + failure
// without dragging real provider secrets into tests.
type fakeProvider struct{ name string }

func (f fakeProvider) Name() string           { return f.name }
func (f fakeProvider) SignatureHeader() string { return "X-Test-Sig" }
func (f fakeProvider) EventIDHeader() string   { return "X-Test-Event-Id" }
func (f fakeProvider) ComputeSignature(body []byte, secret string) string {
	// "signature" = secret + first byte of body (just a stable function).
	if len(body) == 0 {
		return secret
	}
	return secret + string(body[0])
}

func newVerifierNoStore(t *testing.T, cfg Config) *Verifier {
	t.Helper()
	v, err := New(fakeProvider{name: "fake"}, cfg, nil)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return v
}

// ----------------------------------------------------------------------
// Signature happy path
// ----------------------------------------------------------------------

func TestMiddleware_AcceptsValidSignature(t *testing.T) {
	v := newVerifierNoStore(t, Config{Secret: "topsecret"})
	called := false
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if d := DeliveryFromContext(r.Context()); d == nil {
			t.Fatalf("delivery missing from context")
		}
		w.WriteHeader(200)
	}))

	body := []byte(`{"event":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.Header.Set("X-Test-Sig", "topsecret"+string(body[0]))
	req.Header.Set("X-Test-Event-Id", "evt-1")
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Fatal("next handler not called")
	}
}

// ----------------------------------------------------------------------
// Bad signature
// ----------------------------------------------------------------------

func TestMiddleware_RejectsBadSignature(t *testing.T) {
	v := newVerifierNoStore(t, Config{Secret: "topsecret"})
	handler := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler must not be called for bad sig")
	}))
	body := []byte(`{"event":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.Header.Set("X-Test-Sig", "wrong-sig")
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// ----------------------------------------------------------------------
// IP allow-list
// ----------------------------------------------------------------------

func TestMiddleware_IPAllowList(t *testing.T) {
	v := newVerifierNoStore(t, Config{
		Secret:     "topsecret",
		AllowedIPs: []string{"10.0.0.0/8", "192.168.1.42"},
	})
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))

	cases := []struct {
		remote string
		want   int
		name   string
	}{
		{"10.5.5.5:1234", 200, "in CIDR"},
		{"192.168.1.42:5555", 200, "exact host"},
		{"8.8.8.8:5555", 403, "outside allow-list"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(`{"event":"x"}`)
			req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
			req.Header.Set("X-Test-Sig", "topsecret"+string(body[0]))
			req.RemoteAddr = c.remote
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != c.want {
				t.Fatalf("[%s] want %d, got %d body=%s",
					c.remote, c.want, rr.Code, rr.Body.String())
			}
		})
	}
}

// ----------------------------------------------------------------------
// X-Forwarded-For honoured only behind trusted proxies
// ----------------------------------------------------------------------

func TestMiddleware_XForwardedFor_RequiresTrustedProxy(t *testing.T) {
	v := newVerifierNoStore(t, Config{
		Secret:         "topsecret",
		AllowedIPs:     []string{"203.0.113.5"}, // the "real" client IP
		TrustedProxies: []string{"10.0.0.0/8"},
	})
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))

	body := []byte(`{"event":"x"}`)

	// 1. Untrusted proxy: X-F-F is ignored, allow-list sees 8.8.8.8 → 403.
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.Header.Set("X-Test-Sig", "topsecret"+string(body[0]))
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	req.RemoteAddr = "8.8.8.8:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("untrusted proxy must be 403, got %d", rr.Code)
	}

	// 2. Trusted proxy: X-F-F honored → allow-list matches → 200.
	req = httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.Header.Set("X-Test-Sig", "topsecret"+string(body[0]))
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	req.RemoteAddr = "10.5.5.5:1234"
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("trusted-proxy X-F-F must pass, got %d body=%s",
			rr.Code, rr.Body.String())
	}
}

// ----------------------------------------------------------------------
// Method gate
// ----------------------------------------------------------------------

func TestMiddleware_RejectsNonPost(t *testing.T) {
	v := newVerifierNoStore(t, Config{Secret: "x"})
	handler := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("unreachable")
	}))
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/hook", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s want 405, got %d", m, rr.Code)
		}
	}
}

// ----------------------------------------------------------------------
// Body cap
// ----------------------------------------------------------------------

func TestMiddleware_RejectsOversizedBody(t *testing.T) {
	v := newVerifierNoStore(t, Config{Secret: "x", MaxBodyBytes: 64})
	handler := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("unreachable")
	}))
	body := bytes.Repeat([]byte("A"), 65)
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.Header.Set("X-Test-Sig", "x"+string(body[0]))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", rr.Code)
	}
}

// ----------------------------------------------------------------------
// Signature disabled when secret is empty (local dev mode)
// ----------------------------------------------------------------------

func TestMiddleware_DevModeNoSecret(t *testing.T) {
	v := newVerifierNoStore(t, Config{}) // no secret
	called := false
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.RemoteAddr = "1.2.3.4:5555"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 200 || !called {
		t.Fatalf("dev mode should let request through: code=%d called=%v",
			rr.Code, called)
	}
}

// ----------------------------------------------------------------------
// HMAC helpers
// ----------------------------------------------------------------------

func TestHMACHex_Deterministic(t *testing.T) {
	a := HMACHex([]byte("hello"), "secret")
	b := HMACHex([]byte("hello"), "secret")
	if a != b {
		t.Fatalf("HMAC not stable: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("hex sha256 must be 64 chars, got %d", len(a))
	}
	if !CompareConstantTime(a, b) {
		t.Fatal("constant-time compare must match")
	}
	if CompareConstantTime(a, "tampered") {
		t.Fatal("constant-time compare false positive")
	}
}

// ----------------------------------------------------------------------
// XenditHMACProvider sanity
// ----------------------------------------------------------------------

func TestXenditHMACProvider_SignsBody(t *testing.T) {
	p := XenditHMACProvider{}
	body := []byte(`{"amount":1000}`)
	sig := p.ComputeSignature(body, "shh")
	if !strings.HasPrefix(sig, HMACHex(body, "shh")) {
		t.Fatalf("Xendit HMAC mismatch: %s", sig)
	}
}

// readContext is a small linting helper that proves our context key is
// not exported.
func TestDeliveryFromContext_Empty(t *testing.T) {
	if DeliveryFromContext(context.Background()) != nil {
		t.Fatal("empty ctx must return nil delivery")
	}
}

// Confirm io.Reader-based body construction works (used in tests).
func TestReadBuf_IsReader(t *testing.T) {
	var _ io.Reader = readBuf([]byte("x"))
}
