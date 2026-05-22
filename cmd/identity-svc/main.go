// identity-svc is the Platform Foundation service.
// It owns: auth (login/refresh/logout/me), users, branches, RBAC, audit trail.
//
// This is the only service that mints JWTs — every other service uses the
// verifier in pkg/auth to validate incoming tokens.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	identityhttp "github.com/ion-core/backend/internal/identity/adapter/http"
	identitypg "github.com/ion-core/backend/internal/identity/adapter/postgres"
	"github.com/ion-core/backend/internal/identity/usecase"
	platformhttp "github.com/ion-core/backend/internal/platform/adapter/http"
	platformpg "github.com/ion-core/backend/internal/platform/adapter/postgres"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("IDENTITY_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "identity-svc")
	log.Info("starting identity-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- Driven adapters ---
	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	userRepo := identitypg.NewUserRepository(pool)
	roleRepo := identitypg.NewRoleRepository(pool)
	branchRepo := identitypg.NewBranchRepository(pool)
	auditRepo := identitypg.NewAuditRepository(pool)
	configRepo := identitypg.NewPlatformConfigRepository(pool)
	refreshRepo := identitypg.NewRefreshTokenRepository(pool)
	hasher := identitypg.NewBcryptHasher()

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAccessTTL)
	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)
	tokens := identitypg.NewJWTIssuer(issuer)

	// --- Use case ---
	availabilityRepo := identitypg.NewAvailabilityRepository(pool)
	// Wave 81 (TC-USR-019) — mutating user-management calls emit
	// audit_logs rows via this writer.
	auditWriter := auditpg.NewWriter(pool)
	svc := usecase.NewService(userRepo, roleRepo, branchRepo, auditRepo, configRepo, refreshRepo, hasher, tokens, cfg.JWTRefreshTTL, log).
		WithAvailability(availabilityRepo).
		WithAudit(auditWriter)

	// --- Driving adapter (HTTP) ---
	// Per-IP rate limit on /auth/login: burst 10 attempts, refill 0.5/sec
	// (= 1 attempt every 2s sustained). Slows credential stuffing from a
	// single source; upstream WAF/CDN handles broader attacks.
	loginRL := httpserver.NewRateLimit(10, 0.5)
	handler := identityhttp.NewHandler(svc, verifier).WithLoginRateLimit(loginRL)

	// --- Platform/Schema System v1 ---
	// Module-agnostic versioned rule sets (billing / commission /
	// suspension / service). Mounted on identity-svc because it's the
	// platform-config-adjacent service; the api-gateway proxies
	// /api/platform → identity-svc.
	schemaRepo := platformpg.NewSchemaRepository(pool)
	overrideRepo := platformpg.NewOverrideRepository(pool)
	platformSvc := platformusecase.NewService(schemaRepo, overrideRepo)
	platformHandler := platformhttp.NewHandler(platformSvc, verifier)

	// Priority-followup handler — push token registration + HRIS sync.
	priorityHandler := identityhttp.NewPriorityHandler(pool, verifier)

	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("identity-svc", pool.Ping)
	handler.Mount(server.Router)
	platformHandler.Mount(server.Router)
	priorityHandler.Mount(server.Router)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("identity-svc stopped")
}
