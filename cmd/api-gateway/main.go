// api-gateway is the public-facing HTTP entrypoint.
//
// In Phase 1 it does two things:
//  1. Reverse-proxies requests to the right internal service by URL prefix.
//  2. Validates JWTs once at the edge and forwards verified claims downstream.
//
// As we add bounded contexts we add their backend route here.
//
// Why a gateway: the frontend talks to one URL, regardless of how many
// services exist behind it. When a context splits to its own deployment,
// only the gateway config changes.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("API_GATEWAY_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "api-gateway")
	log.Info("starting api-gateway", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("api-gateway", nil) // liveness only — no DB

	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	// Gateway-side metrics tell us the user-perceived latency (including
	// proxy hop) while the per-service /metrics give us the inside view.
	server.Router.Use(httpserver.PrometheusMiddleware("api-gateway"))
	server.Router.Handle("/metrics", httpserver.MetricsHandler())

	// Route table: URL prefix → upstream service.
	// Add more entries as bounded contexts come online.
	upstreams := map[string]string{
		"/api/identity":  fmt.Sprintf("http://localhost:%s", getenv("IDENTITY_SVC_PORT", "8081")),
		"/api/platform":  fmt.Sprintf("http://localhost:%s", getenv("IDENTITY_SVC_PORT", "8081")),
		"/api/network":   fmt.Sprintf("http://localhost:%s", getenv("NETWORK_SVC_PORT", "8085")),
		"/api/warehouse": fmt.Sprintf("http://localhost:%s", getenv("WAREHOUSE_SVC_PORT", "8086")),
		"/api/crm":       fmt.Sprintf("http://localhost:%s", getenv("CRM_SVC_PORT", "8083")),
		// /portal/* is a passthrough — no /api/ prefix on purpose so
		// the customer_app can deep-link the same URL the web portal
		// uses. The upstream crm-svc handler mounts on `/portal/...`,
		// so we skip the prefix strip below.
		"/portal":        fmt.Sprintf("http://localhost:%s", getenv("CRM_SVC_PORT", "8083")),
		"/api/field":      fmt.Sprintf("http://localhost:%s", getenv("FIELD_SVC_PORT", "8087")),
		"/api/uploads":    fmt.Sprintf("http://localhost:%s", getenv("FIELD_SVC_PORT", "8087")),
		// Wave 65 — Operations module (Phase 1A closure). Hosted in
		// field-svc alongside maintenance_events; gateway strips the
		// /api/operations prefix exactly like /api/field.
		"/api/operations": fmt.Sprintf("http://localhost:%s", getenv("FIELD_SVC_PORT", "8087")),
		"/api/billing":    fmt.Sprintf("http://localhost:%s", getenv("BILLING_SVC_PORT", "8084")),
		"/api/enterprise": fmt.Sprintf("http://localhost:%s", getenv("ENTERPRISE_SVC_PORT", "8088")),
	}

	for prefix, target := range upstreams {
		u, err := url.Parse(target)
		if err != nil {
			log.Error("invalid upstream", "prefix", prefix, "target", target)
			os.Exit(1)
		}
		proxy := httputil.NewSingleHostReverseProxy(u)
		// Strip any CORS headers the upstream service set. Both the
		// gateway and each upstream service share the same httpserver
		// middleware stack, so without this we'd end up duplicating
		// every Access-Control-* header (browsers reject any
		// Allow-Origin with more than one value). The gateway is the
		// only place that should speak CORS to the SPA.
		proxy.ModifyResponse = stripUpstreamCORS
		// Strip the prefix before forwarding so the downstream service sees its own routes.
		// e.g. /api/identity/auth/login → /auth/login on identity-svc.
		//
		// Exception: /portal (the customer-self-service surface) wants
		// to keep its prefix because the upstream handler mounts on
		// /portal/* and the web portal already routes /portal/* the
		// same way. Passing through verbatim keeps the URL shape
		// identical between the public portal and the customer_app.
		var stripped http.Handler = stripPrefix(prefix, proxy)
		if prefix == "/portal" {
			stripped = proxy
		}
		server.Router.Mount(prefix, stripped)
		log.Info("gateway route mounted", "prefix", prefix, "upstream", target)
	}

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("api-gateway stopped")
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// stripUpstreamCORS removes Access-Control-* response headers that the
// upstream service set. The gateway re-adds the canonical ones via its
// own chi/cors middleware; without this, both layers contribute and the
// browser sees duplicated Allow-Origin values.
func stripUpstreamCORS(resp *http.Response) error {
	for h := range resp.Header {
		if len(h) >= 14 && (h[:14] == "Access-Control" || h[:14] == "access-control") {
			resp.Header.Del(h)
		}
	}
	return nil
}
