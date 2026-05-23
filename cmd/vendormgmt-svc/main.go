// vendor-svc — Provider & Vendor Input bounded context service.
//
// Wave 107 scope: Provider registry + Capability tags + cost-input
// submission state machine + daily performance metrics. Isolated
// bounded context: this service speaks only to its own `vendor.*`
// schema. It does NOT cross-import from internal/crm, internal/warehouse,
// internal/enterprise. The cron deriver reaches across to enterprise
// tables (ewo_completion_log) via raw SQL — that's deliberate: vendor
// is a downstream metric sink, and the upstream context owns the audit
// row. When vendor-svc gets its own DB (future split), the deriver
// gets a real cross-service port.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	vendorhttp "github.com/ion-core/backend/internal/vendormgmt/adapter/http"
	vendorpg "github.com/ion-core/backend/internal/vendormgmt/adapter/postgres"
	vendorcron "github.com/ion-core/backend/internal/vendormgmt/cron"
	vendorusecase "github.com/ion-core/backend/internal/vendormgmt/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("VENDOR_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "vendor-svc")
	log.Info("starting vendor-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories — one per aggregate.
	providerRepo := vendorpg.NewProviderRepository(pool)
	capabilityRepo := vendorpg.NewProviderCapabilityRepository(pool)
	submissionRepo := vendorpg.NewSubmissionRepository(pool)
	metricsRepo := vendorpg.NewMetricsRepository(pool)

	// Usecases.
	providerSvc := vendorusecase.NewProviderService(providerRepo, capabilityRepo)
	submissionSvc := vendorusecase.NewSubmissionService(submissionRepo, providerRepo)
	metricsSvc := vendorusecase.NewMetricsService(metricsRepo, providerRepo)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	handler := vendorhttp.NewHandler(providerSvc, submissionSvc, metricsSvc, verifier)

	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("vendor-svc", pool.Ping)
	server.Router.Use(httpserver.PrometheusMiddleware("vendor-svc"))
	server.Router.Handle("/metrics", httpserver.MetricsHandler())
	handler.Mount(server.Router)

	// Daily metrics deriver — pulls EWO completions from enterprise.* and
	// upserts per-provider daily KPI rows. Idempotent via the unique
	// (provider_id, metric_date) constraint.
	vendorcron.NewMetricsDeriverDaily(pool, metricsSvc, log).Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("vendor-svc stopped")
}
