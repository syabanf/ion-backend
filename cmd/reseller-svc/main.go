// reseller-svc — Reseller bounded context service.
//
// Wave 94 scope: Reseller Onboarding + Wholesale Supply (admin) +
// Reseller Platform (tenant-scoped public surface). Isolated bounded
// context: this service speaks only to its own `reseller.*` schema.
// It does NOT cross-import from internal/crm, internal/warehouse,
// internal/enterprise. Cross-context UUIDs (parent_subsidiary_id,
// supplier_subsidiary_id) are stored as plain UUIDs and resolved by
// the calling service at display time. This keeps the future
// extraction into a standalone microservice trivial.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	resellerhttp "github.com/ion-core/backend/internal/reseller/adapter/http"
	resellerpartnership "github.com/ion-core/backend/internal/reseller/adapter/partnership"
	resellerpg "github.com/ion-core/backend/internal/reseller/adapter/postgres"
	resellerusecase "github.com/ion-core/backend/internal/reseller/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("RESELLER_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "reseller-svc")
	log.Info("starting reseller-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories — one per aggregate, all bound to the `reseller`
	// schema. Order doesn't matter; cross-repo composition happens in
	// the usecase services below.
	accountRepo := resellerpg.NewResellerAccountRepository(pool)
	skuRepo := resellerpg.NewWholesaleSKURepository(pool)
	orderRepo := resellerpg.NewWholesaleOrderRepository(pool)
	sessionRepo := resellerpg.NewPlatformSessionRepository(pool)
	// Wave 102 — subscriber CRUD + invoice inbox + import audit.
	subscriberRepo := resellerpg.NewSubscriberRepository(pool)
	invoiceRepo := resellerpg.NewSubscriberInvoiceRepository(pool)
	importRepo := resellerpg.NewSubscriberImportRepository(pool)

	// Wave 102 — cross-context compliance read-through. The adapter
	// degrades to KindUnavailable if migration 0066 (partnership) is
	// not applied; the DashboardService catches that and returns
	// compliance_status="unavailable" rather than failing the request.
	complianceReader := resellerpartnership.NewComplianceReader(pool)

	// Usecases — onboarding (admin), wholesale (admin + platform), and
	// platform (auth glue). The platform service uses cfg.JWTSecret as
	// the pepper for the stub shared-secret derivation; the real OAuth
	// flow lands in a later wave.
	onboarding := resellerusecase.NewOnboardingService(accountRepo)
	wholesale := resellerusecase.NewWholesaleService(skuRepo, orderRepo, accountRepo)
	platform := resellerusecase.NewPlatformService(sessionRepo, accountRepo, cfg.JWTSecret)

	// Wave 102 services. Each takes resellerhttp.TenantFromContext as
	// the per-request tenant resolver so the usecase stays free of
	// HTTP imports while still scoping every read/write.
	subscribers := resellerusecase.NewSubscriberService(subscriberRepo, importRepo, resellerhttp.TenantFromContext)
	invoiceInbox := resellerusecase.NewInvoiceInboxService(invoiceRepo, resellerhttp.TenantFromContext)
	dashboard := resellerusecase.NewDashboardService(subscriberRepo, invoiceRepo, complianceReader, resellerhttp.TenantFromContext)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	// Two handlers share the same chi.Router so a single
	// cors/request-id chain applies to both surfaces. Wave 102
	// extensions are attached via WithPlatformExtensions before Mount.
	adminHandler := resellerhttp.NewAdminHandler(onboarding, wholesale, verifier)
	platformHandler := resellerhttp.
		NewPlatformHandler(platform, onboarding, wholesale).
		WithPlatformExtensions(subscribers, invoiceInbox, dashboard)

	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("reseller-svc", pool.Ping)
	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	server.Router.Use(httpserver.PrometheusMiddleware("reseller-svc"))
	server.Router.Handle("/metrics", httpserver.MetricsHandler())
	adminHandler.Mount(server.Router)
	platformHandler.Mount(server.Router)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("reseller-svc stopped")
}
