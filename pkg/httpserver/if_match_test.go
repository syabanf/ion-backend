package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — If-Match middleware contract tests
//
// Covers TC-SM/Edge #21 (Stale Optimistic-Lock Retry) and the cross-
// cutting "Optimistic Lock Stale Version" audit doc entry.
// =====================================================================

func newIfMatchRouter(rv RowVersionFunc) chi.Router {
	r := chi.NewRouter()
	r.Route("/items/{id}", func(r chi.Router) {
		r.Use(RequireIfMatch(rv))
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		r.Patch("/", func(w http.ResponseWriter, r *http.Request) {
			// Echo the resolved version so the test can assert it
			// landed in context.
			w.Header().Set("X-Resolved-Version", IfMatchVersionFromContext(r.Context()))
			w.Header().Set("ETag", EtagFor("v2"))
			w.WriteHeader(http.StatusOK)
		})
		r.Put("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		r.Delete("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	return r
}

// Fixed-version stub: always returns "v1" so the test can assert the
// happy/sad-path semantics without DB.
func stubVersion(_ context.Context, _ uuid.UUID) (string, error) {
	return "v1", nil
}

func TestRequireIfMatch_Table(t *testing.T) {
	id := uuid.New()
	r := newIfMatchRouter(stubVersion)
	cases := []struct {
		name        string
		method      string
		header      string
		wantStatus  int
		wantBodyHas string
	}{
		{
			name:        "missing header on PATCH -> 428 required",
			method:      http.MethodPatch,
			header:      "",
			wantStatus:  http.StatusPreconditionRequired,
			wantBodyHas: "if_match.required",
		},
		{
			name:        "missing header on PUT -> 428 required",
			method:      http.MethodPut,
			header:      "",
			wantStatus:  http.StatusPreconditionRequired,
			wantBodyHas: "if_match.required",
		},
		{
			name:        "missing header on DELETE -> 428 required",
			method:      http.MethodDelete,
			header:      "",
			wantStatus:  http.StatusPreconditionRequired,
			wantBodyHas: "if_match.required",
		},
		{
			name:        "stale weak-etag -> 412 stale",
			method:      http.MethodPatch,
			header:      `W/"v0"`,
			wantStatus:  http.StatusPreconditionFailed,
			wantBodyHas: "if_match.stale",
		},
		{
			name:        "stale bare-quoted etag -> 412 stale",
			method:      http.MethodPatch,
			header:      `"v0"`,
			wantStatus:  http.StatusPreconditionFailed,
			wantBodyHas: "if_match.stale",
		},
		{
			name:       "matching weak-etag -> 200",
			method:     http.MethodPatch,
			header:     `W/"v1"`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "matching bare-quoted etag -> 200",
			method:     http.MethodPatch,
			header:     `"v1"`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "matching unquoted token -> 200 (CLI ergonomics)",
			method:     http.MethodPatch,
			header:     `v1`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET passes through without header",
			method:     http.MethodGet,
			header:     "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET with stale header still 200 (non-mutating)",
			method:     http.MethodGet,
			header:     `W/"v0"`,
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/items/"+id.String()+"/", nil)
			if tc.header != "" {
				req.Header.Set("If-Match", tc.header)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantBodyHas != "" && !strings.Contains(w.Body.String(), tc.wantBodyHas) {
				t.Fatalf("body does not contain %q; got %q", tc.wantBodyHas, w.Body.String())
			}
		})
	}
}

func TestRequireIfMatch_ContextCarriesVersion(t *testing.T) {
	id := uuid.New()
	r := newIfMatchRouter(stubVersion)
	req := httptest.NewRequest(http.MethodPatch, "/items/"+id.String()+"/", nil)
	req.Header.Set("If-Match", `W/"v1"`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Resolved-Version"); got != "v1" {
		t.Errorf("X-Resolved-Version = %q, want v1", got)
	}
	if got := w.Header().Get("ETag"); got != `W/"v2"` {
		t.Errorf("response ETag = %q, want W/\"v2\"", got)
	}
}

// Edge #21 — stale-then-fresh retry. First PATCH with v0 gets 412,
// client re-reads + retries with v1 and succeeds.
func TestRequireIfMatch_StaleRetrySucceeds(t *testing.T) {
	id := uuid.New()
	r := newIfMatchRouter(stubVersion)

	// 1st attempt — stale.
	req1 := httptest.NewRequest(http.MethodPatch, "/items/"+id.String()+"/", nil)
	req1.Header.Set("If-Match", `W/"v0"`)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusPreconditionFailed {
		t.Fatalf("1st attempt status = %d, want 412", w1.Code)
	}

	// 2nd attempt — fresh.
	req2 := httptest.NewRequest(http.MethodPatch, "/items/"+id.String()+"/", nil)
	req2.Header.Set("If-Match", `W/"v1"`)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("2nd attempt status = %d, want 200; body=%q", w2.Code, w2.Body.String())
	}
}

func TestRequireIfMatch_InvalidUUID(t *testing.T) {
	r := newIfMatchRouter(stubVersion)
	req := httptest.NewRequest(http.MethodPatch, "/items/not-a-uuid/", nil)
	req.Header.Set("If-Match", `W/"v1"`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", w.Code, w.Body.String())
	}
}

func TestRequireIfMatch_LookupError(t *testing.T) {
	// Simulate a NotFound from the row-version lookup — middleware
	// surfaces it through WriteError, which maps to 404.
	rv := func(_ context.Context, _ uuid.UUID) (string, error) {
		return "", derrors.NotFound("item.not_found", "item not found")
	}
	r := chi.NewRouter()
	r.With(RequireIfMatch(rv)).Patch("/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	id := uuid.New()
	req := httptest.NewRequest(http.MethodPatch, "/items/"+id.String(), nil)
	req.Header.Set("If-Match", `W/"v1"`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestEtagFor(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"v1", `W/"v1"`},
		{"abc-123", `W/"abc-123"`},
		// strconv.Quote escapes inner quotes — important for raw row-version values
		{`a"b`, `W/"a\"b"`},
	}
	for _, tc := range cases {
		got := EtagFor(tc.in)
		if got != tc.want {
			t.Errorf("EtagFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseIfMatchTag(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`W/"v1"`, "v1"},
		{`w/"v1"`, "v1"},
		{`"v1"`, "v1"},
		{`v1`, "v1"},
		{` W/"v1" `, "v1"},
	}
	for _, tc := range cases {
		got := parseIfMatchTag(tc.in)
		if got != tc.want {
			t.Errorf("parseIfMatchTag(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsMutating(t *testing.T) {
	if !isMutating(http.MethodPost) {
		t.Error("POST should be mutating")
	}
	if !isMutating(http.MethodPut) {
		t.Error("PUT should be mutating")
	}
	if !isMutating(http.MethodPatch) {
		t.Error("PATCH should be mutating")
	}
	if !isMutating(http.MethodDelete) {
		t.Error("DELETE should be mutating")
	}
	if isMutating(http.MethodGet) {
		t.Error("GET should NOT be mutating")
	}
	if isMutating(http.MethodHead) {
		t.Error("HEAD should NOT be mutating")
	}
	if isMutating(http.MethodOptions) {
		t.Error("OPTIONS should NOT be mutating")
	}
}
