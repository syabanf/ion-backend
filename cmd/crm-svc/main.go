// crm-svc — Customer Relationship Management service.
//
// Round-1 scope: broadband lead lifecycle (capture, coverage snapshot,
// document checklist, conversion to customer+order). Round 2 adds the
// self-order portal, sales-app, KTP OCR, sales-rep type enforcement,
// dashboards, and read-only commission visibility.
//
// The coverage check is reached via an in-process gateway to the network
// usecase, so this binary embeds its own minimal network wiring. When the
// network service moves out to its own process, the gateway swaps to HTTP
// without touching the CRM usecase.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	crmhttp "github.com/ion-core/backend/internal/crm/adapter/http"
	billingpg "github.com/ion-core/backend/internal/billing/adapter/postgres"
	billingusecase "github.com/ion-core/backend/internal/billing/usecase"
	crmbilling "github.com/ion-core/backend/internal/crm/adapter/billing"
	crmid "github.com/ion-core/backend/internal/crm/adapter/identity"
	crmnet "github.com/ion-core/backend/internal/crm/adapter/network"
	crmplatform "github.com/ion-core/backend/internal/crm/adapter/platform"
	crmpg "github.com/ion-core/backend/internal/crm/adapter/postgres"
	crmusecase "github.com/ion-core/backend/internal/crm/usecase"
	networkpg "github.com/ion-core/backend/internal/network/adapter/postgres"
	networkradius "github.com/ion-core/backend/internal/network/adapter/radius"
	networkusecase "github.com/ion-core/backend/internal/network/usecase"
	platformcrm "github.com/ion-core/backend/internal/platform/adapter/crm"
	platformpg "github.com/ion-core/backend/internal/platform/adapter/postgres"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/cryptutil"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/notifyx"
	"github.com/ion-core/backend/pkg/platformconfig"
)

func main() {
	cfg, err := config.Load("CRM_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "crm-svc")
	log.Info("starting crm-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// At-rest encryption for KTP (NIK). When KTP_ENC_KEY is unset we
	// run in legacy plaintext mode so local dev + the e2e harness
	// keep working without operator setup — production binaries must
	// set the env var.
	var sealer *cryptutil.Sealer
	if k := os.Getenv("KTP_ENC_KEY"); k != "" {
		s, err := cryptutil.NewSealer(k)
		if err != nil {
			log.Error("KTP_ENC_KEY invalid", "err", err)
			os.Exit(1)
		}
		sealer = s
		log.Info("KTP at-rest encryption enabled")
	}

	// CRM repos
	productRepo := crmpg.NewProductRepository(pool)
	leadRepo := crmpg.NewLeadRepository(pool).WithSealer(sealer)
	docRepo := crmpg.NewDocumentRepository(pool)
	customerRepo := crmpg.NewCustomerRepository(pool).WithSealer(sealer)
	orderRepo := crmpg.NewOrderRepository(pool)

	// In-process network service to back the coverage gateway. When this
	// stack splits, drop this block and point the gateway at an HTTP client.
	nodeTypeRepo := networkpg.NewNodeTypeRepository(pool)
	nodeRepo := networkpg.NewNodeRepository(pool)
	portRepo := networkpg.NewPortRepository(pool)
	coverageRepo := networkpg.NewCoverageRepository(pool)
	impactRepo := networkpg.NewImpactRepository(pool)
	radiusClient := networkradius.NewLocalClient(pool, log).WithAudit(auditpg.NewWriter(pool))
	configReader := platformconfig.New(pool)
	netSvc := networkusecase.NewService(
		nodeTypeRepo, nodeRepo, portRepo,
		coverageRepo, impactRepo, radiusClient,
		configReader, log,
	)

	coverageGW := crmnet.NewCoverageGateway(netSvc)

	// Platform Schema System v1 resolver. CRM only needs this so the
	// in-process billing usecase below picks up per-customer schemas
	// when computing OTC commission on lead conversion. Same pool
	// (single DB today); HTTP-backed implementation later.
	platformSchemaRepo := platformpg.NewSchemaRepository(pool)
	platformOverrideRepo := platformpg.NewOverrideRepository(pool)
	// Wave 82 Tier 2c — auto-load customer lock so every downstream
	// kind (billing / commission / service / suspension) honors the
	// Wave 80b snapshot without each caller having to thread the lock
	// id through ResolveOptions.
	platformLockReader := platformcrm.NewLockReader(pool)
	platformSvc := platformusecase.NewService(platformSchemaRepo, platformOverrideRepo).
		WithCustomerLockReader(platformLockReader)
	schemaResolver := platformusecase.NewResolver(platformSvc)

	// In-process billing usecase so converting a lead auto-creates the
	// OTC invoice. When billing splits to its own process, swap this for
	// an HTTP-backed BillingGateway implementation.
	billingSvc := billingusecase.NewService(
		billingpg.NewInvoiceRepository(pool),
		billingpg.NewPaymentRepository(pool),
	).WithSchemaResolver(schemaResolver)
	billingGW := crmbilling.NewGateway(billingSvc)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	// M4 r2 — onboarding schemas + sales-type enforcement.
	schemaRepo := crmpg.NewOnboardingSchemaRepository(pool)
	salesUserGW := crmid.NewSalesUserGateway(pool)

	// Wave 80b — schema-resolver gateway for lead-conversion lock snapshot.
	// At convert, the resolver picks the version of each of the 5 schema
	// kinds and CRM persists the IDs onto the new customer record so
	// later publishes don't retro-rate them (TC-SCH-011/023/026, TC-PRD-025).
	crmSchemaResolver := crmplatform.NewSchemaResolverGateway(platformSvc)

	// Wave 81 (TC-PRD-013/028) — product mutations write through
	// pkg/audit so the admin viewer can render product history.
	crmAuditWriter := auditpg.NewWriter(pool)

	svc := crmusecase.NewService(productRepo, leadRepo, docRepo, customerRepo, orderRepo, coverageGW).
		WithBilling(billingGW).
		WithR2(schemaRepo, salesUserGW).
		WithSchemaResolver(crmSchemaResolver).
		WithAudit(crmAuditWriter)

	// Tunes: burst=6 lets a rep batch six KTPs quickly, refill=0.1/s caps
	// sustained throughput at six per minute — well above onboarding
	// cadence but tight enough to throttle a stolen-credentials replay.
	ktpRL := httpserver.NewRateLimit(6, 0.1)
	handler := crmhttp.NewHandler(svc, verifier).WithKTPRateLimit(ktpRL).WithEventPool(pool)
	// Override the default stub KTP provider when ops asks for one.
	// Available providers are gated by build tag — `tesseract` requires
	// `go build -tags=tesseract` (see ktp_ocr_provider_tesseract.go).
	//
	// FAIL-FAST: if KTP_OCR_PROVIDER is set to a non-stub name AND
	// the build doesn't include that provider, the service refuses
	// to start. Silently using the stub on a binary that was meant
	// to use real OCR would produce fake-but-plausible KYC results —
	// strictly worse than a loud crash. Set
	// KTP_OCR_PROVIDER_PERMISSIVE=true to keep the old "fall back to
	// stub" behavior (useful in dev / CI without the tesseract image).
	if name := os.Getenv("KTP_OCR_PROVIDER"); name != "" && name != "stub" {
		if p := pickKTPProvider(name); p != nil {
			handler = handler.WithKTPProvider(p)
			log.Info("KTP OCR provider", "name", p.Name())
		} else if os.Getenv("KTP_OCR_PROVIDER_PERMISSIVE") == "true" {
			log.Warn("KTP_OCR_PROVIDER not compiled in; falling back to stub (permissive mode)",
				"name", name)
		} else {
			log.Error("KTP_OCR_PROVIDER not compiled in — refusing to start",
				"name", name,
				"remedy", "rebuild with `-tags="+name+"` or set KTP_OCR_PROVIDER_PERMISSIVE=true",
			)
			os.Exit(1)
		}
	}
	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("crm-svc", pool.Ping)
	handler.Mount(server.Router)

	// Wave 83 (TC-RAD-013/014/015) — RADIUS profile-refresh hook for
	// the Phase 2 + Portal handlers. Wraps the same in-process
	// radiusClient that already audits transitions; once FreeRADIUS
	// lands the gateway will push real CoA packets.
	radiusGW := crmnet.NewRadiusGateway(radiusClient)

	// Phase 2 — post-activation surface (add-ons, plan changes,
	// relocations). Lives in its own handler because the schema is
	// new and the architecture pattern is intentionally simpler
	// (direct pgxpool) until volumes justify hexagonal layering.
	crmhttp.NewPhase2Handler(pool, verifier).
		WithRadiusGateway(radiusGW).
		Mount(server.Router)

	// Push-notification dispatcher. Default provider is the
	// log-only stub; flip to FCM/APNS via WithProvider once the
	// service-account JSON lands (see docs/backlog.md §FCM/APNS).
	notifier := notifyx.New(pool, log)
	log.Info("notifyx dispatcher wired", "provider", "stub")

	// Customer portal — OTP login + customer-scoped JWT + self-
	// service reads (services / invoices / tickets). Routes under
	// /portal/* are public for auth, JWT-gated for data.
	portalIssuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAccessTTL)
	crmhttp.NewPortalHandler(pool, verifier, portalIssuer).
		WithNotifier(notifier).
		WithRadiusGateway(radiusGW).
		Mount(server.Router)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("crm-svc stopped")
}
