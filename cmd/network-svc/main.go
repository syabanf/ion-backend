// network-svc — Network & Orchestration service.
//
// Owns: topology registry (nodes, ports, node types), coverage check,
// coverage polygons (PostGIS), KMZ/KML import, downstream fault impact,
// RADIUS account state (via local DB-backed adapter).
//
// VLAN / IP pool allocation and the automated onboarding pipeline ride
// on top of these primitives — they land when CRM customers exist (M4).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	networkhttp "github.com/ion-core/backend/internal/network/adapter/http"
	networkpg "github.com/ion-core/backend/internal/network/adapter/postgres"
	networkradius "github.com/ion-core/backend/internal/network/adapter/radius"
	networkusecase "github.com/ion-core/backend/internal/network/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/platformconfig"
)

func main() {
	cfg, err := config.Load("NETWORK_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "network-svc")
	log.Info("starting network-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- Driven adapters ---
	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	nodeTypeRepo := networkpg.NewNodeTypeRepository(pool)
	nodeRepo := networkpg.NewNodeRepository(pool)
	portRepo := networkpg.NewPortRepository(pool)
	coverageRepo := networkpg.NewCoverageRepository(pool)
	impactRepo := networkpg.NewImpactRepository(pool)
	// Wave 81 (TC-RAD-021) — wire audit writer into the RADIUS
	// client so every state transition (provision / promote / suspend
	// / restore / deactivate) lands a row in identity.audit_logs.
	radiusClient := networkradius.NewLocalClient(pool, log).WithAudit(auditpg.NewWriter(pool))
	configReader := platformconfig.New(pool)

	// JWT verifier — identity-svc issued the token; we just validate locally.
	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	// --- Use case ---
	svc := networkusecase.NewService(
		nodeTypeRepo, nodeRepo, portRepo,
		coverageRepo, impactRepo, radiusClient,
		configReader, log,
	)

	// --- Driving adapter (HTTP) ---
	handler := networkhttp.NewHandler(svc, verifier)
	priorityHandler := networkhttp.NewPriorityHandler(pool, verifier)
	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("network-svc", pool.Ping)
	handler.Mount(server.Router)
	priorityHandler.Mount(server.Router)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("network-svc stopped")
}
