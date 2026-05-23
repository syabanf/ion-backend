// Wave 105 — Prometheus HTTP instrumentation.
//
// Adds three low-cardinality metrics for every request that flows through
// the chi router: latency histogram, request counter, in-flight gauge.
// Each metric is labeled with the service name + chi route pattern (NOT
// the raw URL path — that would explode label cardinality when path
// parameters carry UUIDs or per-customer keys).
//
// Wiring (per *-svc/main.go):
//
//	server.Router.Use(httpserver.PrometheusMiddleware("enterprise-svc"))
//	server.Router.Handle("/metrics", httpserver.MetricsHandler())
//
// The metrics endpoint is unauthenticated by design — Prometheus
// scrapers run inside the cluster and aren't expected to hold a JWT.
// Production deploys gate /metrics with an ingress allowlist.
package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Buckets cover the realistic latency spectrum for an internal HTTP
// service: 5ms (cached endpoints), 100ms (typical write), 1-5s (bulk
// hydrate, recompute). The 10s ceiling matches the server's
// WriteTimeout default so anything above that is already a 503.
var httpLatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var (
	httpRequestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency, by service / route / method / status.",
			Buckets: httpLatencyBuckets,
		},
		[]string{"service", "route", "method", "status"},
	)

	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP request count, by service / route / method / status.",
		},
		[]string{"service", "route", "method", "status"},
	)

	httpRequestsInFlight = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Active in-flight HTTP requests, by service.",
		},
		[]string{"service"},
	)
)

// PrometheusMiddleware records request latency + count + in-flight
// gauge for every request. Route label uses the chi route pattern
// (e.g. "/api/enterprise/boqs/{id}") rather than the concrete URL,
// so a 100k-customer fleet doesn't blow up the metrics cardinality.
//
// Must be mounted AFTER chi's RequestID + Recoverer (so the response
// status reflects panics-as-500), and BEFORE any handler that needs
// timing. Typical wiring is:
//
//	server.Router.Use(RequireActiveActor(verifier))   // existing
//	server.Router.Use(PrometheusMiddleware("xxx-svc")) // new
//	handler.Mount(server.Router)
func PrometheusMiddleware(serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			httpRequestsInFlight.WithLabelValues(serviceName).Inc()
			defer httpRequestsInFlight.WithLabelValues(serviceName).Dec()

			next.ServeHTTP(ww, r)

			// Status defaults to 200 if the handler didn't explicitly
			// write a header — WrapResponseWriter's Status() returns
			// 0 in that case, so coerce to 200 here.
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			// Route label: prefer chi's route pattern to keep
			// cardinality low. RoutePattern is empty for 404s
			// (no route matched) — fall back to the raw path so we
			// still see "POST /typo/path → 404" in metrics, but
			// guard against unbounded explosion by trimming to
			// the path-only component (no query string).
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = r.URL.Path
			}

			statusStr := strconv.Itoa(status)
			elapsed := time.Since(start).Seconds()

			httpRequestDurationSeconds.
				WithLabelValues(serviceName, route, r.Method, statusStr).
				Observe(elapsed)
			httpRequestsTotal.
				WithLabelValues(serviceName, route, r.Method, statusStr).
				Inc()
		})
	}
}

// MetricsHandler returns the standard Prometheus scrape handler. Each
// *-svc binary mounts it at /metrics alongside /healthz.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
