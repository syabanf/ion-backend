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
	"strconv"
	"syscall"

	identityhttp "github.com/ion-core/backend/internal/identity/adapter/http"
	identitypg "github.com/ion-core/backend/internal/identity/adapter/postgres"
	"github.com/ion-core/backend/internal/identity/usecase"
	platformdomain "github.com/ion-core/backend/internal/platform/domain"
	platformhttp "github.com/ion-core/backend/internal/platform/adapter/http"
	platformcrm "github.com/ion-core/backend/internal/platform/adapter/crm"
	platformpg "github.com/ion-core/backend/internal/platform/adapter/postgres"
	platformcron "github.com/ion-core/backend/internal/platform/cron"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
	audithttp "github.com/ion-core/backend/pkg/audit/http"
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
	//
	// Wave 130: env-tunable. AUTH_LOGIN_RL_BURST + AUTH_LOGIN_RL_REFILL
	// let SIT/load-test runs lift the cap (set both to e.g. 1000 + 500
	// to effectively disable). Production keeps the defaults.
	rlBurst, rlRefill := 10.0, 0.5
	if v := os.Getenv("AUTH_LOGIN_RL_BURST"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			rlBurst = n
		}
	}
	if v := os.Getenv("AUTH_LOGIN_RL_REFILL"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			rlRefill = n
		}
	}
	loginRL := httpserver.NewRateLimit(rlBurst, rlRefill)
	handler := identityhttp.NewHandler(svc, verifier).WithLoginRateLimit(loginRL)

	// --- Platform/Schema System v1 ---
	// Module-agnostic versioned rule sets (billing / commission /
	// suspension / service). Mounted on identity-svc because it's the
	// platform-config-adjacent service; the api-gateway proxies
	// /api/platform → identity-svc.
	schemaRepo := platformpg.NewSchemaRepository(pool)
	overrideRepo := platformpg.NewOverrideRepository(pool)
	// Wave 82 Tier 2c — customer schema lock reader; resolver honors
	// crm.customers.locked_*_schema_version_id without callers having
	// to pass it through ResolveOptions every time.
	//
	// Wave 116 — Deep schema content validators. The ValidatorRegistry
	// dispatches by kind to typed validators (Onboarding / Billing /
	// Service / Commission / Suspension); results land in
	// platform.schema_validation_results via the new repo. Publish
	// route uses the registry as a pre-publish gate.
	validatorRegistry := platformdomain.NewValidatorRegistry()
	validationResultsRepo := platformpg.NewValidationResultRepository(pool)
	platformSvc := platformusecase.NewService(schemaRepo, overrideRepo).
		WithCustomerLockReader(platformcrm.NewLockReader(pool)).
		WithValidatorRegistry(validatorRegistry, validationResultsRepo)
	platformHandler := platformhttp.NewHandler(platformSvc, verifier)

	// Wave 116 — Nightly platform.schema_validation sweep at 04:00 UTC.
	// Runs ValidateAllPublishedSchemas across all kinds and emits an
	// audit-level WARN when any schema falls below validation. The
	// runner spins a goroutine and exits on context cancellation, so it
	// participates in the normal SIGINT/SIGTERM shutdown.
	platformCronRunner := platformcron.New(pool, log).
		WithValidationSweeper(platformSvc)
	platformCronRunner.Start(ctx)

	// Priority-followup handler — push token registration + HRIS sync.
	priorityHandler := identityhttp.NewPriorityHandler(pool, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "identity-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("identity-svc", pool.Ping)
	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	handler.Mount(server.Router)
	platformHandler.Mount(server.Router)
	priorityHandler.Mount(server.Router)

	// Wave 104 — audit query + chain-verify API. Mounts under /api/audit
	// guarded by identity.audit.read. The reader shares the same pgx pool
	// as the writer; identity-svc is the canonical home because the
	// audit_logs table lives in the identity schema.
	auditReader := auditpg.NewReader(pool)
	auditQueryHandler := audithttp.NewHandler(auditReader, verifier)
	auditQueryHandler.MountAuditRoutes(server.Router)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("identity-svc stopped")
}
