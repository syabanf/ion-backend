package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestPrometheusMiddleware_RecordsRequest exercises the happy path:
// one GET against a registered route, then scrape /metrics and assert
// the route appears under the labels we care about. We don't assert
// on exact bucket counts (Prometheus's text format isn't a stable
// snapshot to match against); we just look for our service + route
// + method label substring.
func TestPrometheusMiddleware_RecordsRequest(t *testing.T) {
	r := chi.NewRouter()
	r.Use(PrometheusMiddleware("test-svc"))
	r.Get("/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Handle("/metrics", MetricsHandler())

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Hit the handled route a couple of times so the histogram is
	// guaranteed to be non-empty by the time we scrape.
	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL + "/things/abc-" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("GET /things: %v", err)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	got := string(body)

	// The chi route pattern — not the concrete URL — must be the
	// label value. This is the cardinality-safety invariant.
	mustContain := []string{
		`service="test-svc"`,
		`route="/things/{id}"`,
		`method="GET"`,
		`http_request_duration_seconds_bucket`,
		`http_requests_total`,
		`http_requests_in_flight`,
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("expected /metrics output to contain %q\n--- body ---\n%s", s, got)
		}
	}

	// Make sure NO concrete path leaked through — that would
	// indicate the chi RoutePattern fallback fired when it
	// shouldn't have.
	if strings.Contains(got, `route="/things/abc-a"`) {
		t.Errorf("metrics body leaks concrete path — cardinality risk:\n%s", got)
	}
}

// TestPrometheusMiddleware_UnmatchedRouteUsesPath confirms the fallback:
// when chi has no matching route, we fall back to the raw URL path so
// the counter still ticks. (No service operates without /metrics in
// prod, so the fallback only fires for 404s.)
func TestPrometheusMiddleware_UnmatchedRouteUsesPath(t *testing.T) {
	r := chi.NewRouter()
	r.Use(PrometheusMiddleware("test-fallback-svc"))
	r.Handle("/metrics", MetricsHandler())

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Hit a non-existent route — chi will respond 404 and
	// RoutePattern will be empty.
	resp, err := http.Get(srv.URL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET 404: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	got := string(body)

	if !strings.Contains(got, `service="test-fallback-svc"`) {
		t.Errorf("expected metrics to record fallback service label:\n%s", got)
	}
	if !strings.Contains(got, `status="404"`) {
		t.Errorf("expected status=\"404\" label, got:\n%s", got)
	}
}
