// field-svc — Technician & Field service.
//
// Round-1 scope: WO lifecycle (create from CRM order → route → assign →
// in_progress → checklist + resolution → BAST submit → NOC verify),
// team management, immutable BAST records. Round 2 adds: Flutter mobile
// app, HRIS availability sync, OTP sign-off, warehouse dispatch QR flow,
// auto-pair on SLA breach, reschedule UI.
//
// The CRM lookup runs through an in-process gateway; this binary embeds
// minimal CRM wiring (which in turn embeds the network wiring it needs).
// When CRM moves to its own deployment, swap the gateway impl to HTTP.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	billingcrm "github.com/ion-core/backend/internal/billing/adapter/crm"
	billingnet "github.com/ion-core/backend/internal/billing/adapter/network"
	billingpg "github.com/ion-core/backend/internal/billing/adapter/postgres"
	billingusecase "github.com/ion-core/backend/internal/billing/usecase"
	crmbilling "github.com/ion-core/backend/internal/crm/adapter/billing"
	crmid "github.com/ion-core/backend/internal/crm/adapter/identity"
	crmnet "github.com/ion-core/backend/internal/crm/adapter/network"
	crmpg "github.com/ion-core/backend/internal/crm/adapter/postgres"
	crmusecase "github.com/ion-core/backend/internal/crm/usecase"
	fieldbilling "github.com/ion-core/backend/internal/field/adapter/billing"
	fieldbranch "github.com/ion-core/backend/internal/field/adapter/branch"
	fieldcrm "github.com/ion-core/backend/internal/field/adapter/crm"
	fieldhttp "github.com/ion-core/backend/internal/field/adapter/http"
	fieldpg "github.com/ion-core/backend/internal/field/adapter/postgres"
	fieldgwnet "github.com/ion-core/backend/internal/field/adapter/network"
	fieldplatform "github.com/ion-core/backend/internal/field/adapter/platform"
	fieldgwuploads "github.com/ion-core/backend/internal/field/adapter/uploads"
	fieldusecase "github.com/ion-core/backend/internal/field/usecase"
	opshttp "github.com/ion-core/backend/internal/operations/adapter/http"
	networkpg "github.com/ion-core/backend/internal/network/adapter/postgres"
	networkradius "github.com/ion-core/backend/internal/network/adapter/radius"
	networkusecase "github.com/ion-core/backend/internal/network/usecase"
	platformcrm "github.com/ion-core/backend/internal/platform/adapter/crm"
	platformpg "github.com/ion-core/backend/internal/platform/adapter/postgres"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
	uploadshttp "github.com/ion-core/backend/internal/uploads/adapter/http"
	uploadspg "github.com/ion-core/backend/internal/uploads/adapter/postgres"
	uploadsusecase "github.com/ion-core/backend/internal/uploads/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/cryptutil"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/notifyx"
	"github.com/ion-core/backend/pkg/platformconfig"
	"github.com/ion-core/backend/pkg/uploads"
)

func main() {
	cfg, err := config.Load("FIELD_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "field-svc")
	log.Info("starting field-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Field repos
	woRepo := fieldpg.NewWORepository(pool)
	assignRepo := fieldpg.NewAssignmentRepository(pool)
	checklistRepo := fieldpg.NewChecklistRepository(pool)
	resolutionRepo := fieldpg.NewResolutionRepository(pool)
	bastRepo := fieldpg.NewBASTRepository(pool)
	teamRepo := fieldpg.NewTeamRepository(pool)

	// CRM wiring (in-process) — needed by the gateway.
	// CRM itself needs the network service for its coverage gateway.
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
	crmCoverageGW := crmnet.NewCoverageGateway(netSvc)

	// At-rest encryption for KTP (NIK). See cmd/crm-svc/main.go for
	// the rationale; field-svc uses the same env var so the two
	// services round-trip the same ciphertext.
	var sealer *cryptutil.Sealer
	if k := os.Getenv("KTP_ENC_KEY"); k != "" {
		s, err := cryptutil.NewSealer(k)
		if err != nil {
			log.Error("KTP_ENC_KEY invalid", "err", err)
			os.Exit(1)
		}
		sealer = s
	}

	productRepo := crmpg.NewProductRepository(pool)
	leadRepo := crmpg.NewLeadRepository(pool).WithSealer(sealer)
	docRepo := crmpg.NewDocumentRepository(pool)
	customerRepo := crmpg.NewCustomerRepository(pool).WithSealer(sealer)
	orderRepo := crmpg.NewOrderRepository(pool)

	// Billing wiring (in-process). field-svc needs it for two cross-cuts:
	//
	//   - the BAST payment-gate (IsOrderOTCPaid)
	//   - the termination-complete hook (CompleteTerminationByWO), which
	//     fires when VerifyBAST approves a wo_type=termination BAST
	//
	// CRM in this binary also gets a billing gateway so CRM-initiated
	// convert flows auto-create OTC invoices.
	//
	// The termination hook needs WithR2 (for the network gateway used to
	// drive RADIUS DEACTIVATED) and WithR3 (for the termination_request
	// repo + the customer SetCustomerStatus call via the CRM gateway).
	// We re-use the same network RadiusClient that field-svc already
	// holds for its own purposes.
	billingSvc := billingusecase.NewService(
		billingpg.NewInvoiceRepository(pool),
		billingpg.NewPaymentRepository(pool),
	).
		WithR2(
			billingpg.NewPolicyRepository(pool),
			billingpg.NewCycleRepository(pool),
			billingpg.NewCommissionRepository(pool),
			billingcrm.New(pool),
			billingnet.New(radiusClient),
			log,
		).
		// field-gateway slot is nil — field-svc is the field. Termination
		// WO creation only fires from billing-svc, not from this binary.
		WithR3(
			billingpg.NewTerminationRepository(pool),
			billingpg.NewReferralRewardRepository(pool),
			nil,
		)
	crmBillingGW := crmbilling.NewGateway(billingSvc)

	// M4 r2 wiring (consistent with crm-svc).
	schemaRepo := crmpg.NewOnboardingSchemaRepository(pool)
	salesUserGW := crmid.NewSalesUserGateway(pool)

	crmSvc := crmusecase.NewService(productRepo, leadRepo, docRepo, customerRepo, orderRepo, crmCoverageGW).
		WithBilling(crmBillingGW).
		WithR2(schemaRepo, salesUserGW)
	// The CRM gateway also writes directly to crm.customers for the
	// install-complete activation hook, so it takes the pool too.
	crmGW := fieldcrm.NewGateway(crmSvc, pool)
	fieldBillingGW := fieldbilling.NewGateway(billingSvc)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	rescheduleRepo := fieldpg.NewRescheduleRepository(pool)

	// M5 r3 — uploads pipeline (local-fs storage) + GPS gateway.
	uploadRoot := getenv("UPLOAD_ROOT", "/tmp/ion-uploads")
	localStore, err := uploads.NewLocalStore(uploadRoot)
	if err != nil {
		log.Error("upload store init failed", "err", err)
		os.Exit(1)
	}
	uploadRepo := uploadspg.NewRepository(pool)
	uploadSvc := uploadsusecase.NewService(localStore, uploadRepo)
	uploadsGW := fieldgwuploads.New(uploadSvc)

	radiusReader := fieldgwnet.NewRadiusReader(radiusClient)
	// Activation gateway — fires from VerifyBAST(approved) on install
	// WOs. Provisions a RADIUS account in TEMPORARY then promotes to
	// PERMANENT_ACTIVE, then the CRM gateway flips the customer.
	activator := fieldgwnet.NewActivator(radiusClient)

	// Wave 65 (Phase 1A closure) — branch resolver implements the three
	// new ports for per-branch SLA, address-to-Sub Area resolution, and
	// Team Leader escalation chain. All three are optional from the
	// usecase's perspective; nil falls back to pre-Wave-65 behavior.
	branchResolver := fieldbranch.New(pool)

	// Wave 81 (TC-TLP-014/022/023) — audit writer + push dispatcher
	// for dispatch mutations. Audit captures every AssignTechnicians
	// call into identity.audit_logs; the dispatcher fans an
	// assignment push to lead + observer (default StubPush logs only —
	// swap to FCM via WithProvider when credentials land).
	fieldAuditW := auditpg.NewWriter(pool)
	fieldNotifier := notifyx.New(pool, log)

	// Wave 84b (TC-WO-011) — service-schema resolver for per-product
	// checklist materialization. Embeds an in-process platform usecase
	// (schemas + overrides + the customer lock reader from Wave 82
	// Tier 2c) wrapped by the field-side adapter that translates the
	// schema content's checklist_items array to checklist template
	// rows. Nil-safe in the field service — without this, the WO
	// checklist falls back to the legacy per-product_type templates.
	platformSchemaRepo := platformpg.NewSchemaRepository(pool)
	platformOverrideRepo := platformpg.NewOverrideRepository(pool)
	platformSvc := platformusecase.NewService(platformSchemaRepo, platformOverrideRepo).
		WithCustomerLockReader(platformcrm.NewLockReader(pool))
	fieldServiceSchemaResolver := fieldplatform.NewServiceSchemaResolver(
		platformusecase.NewResolver(platformSvc))

	svc := fieldusecase.NewService(woRepo, assignRepo, checklistRepo, resolutionRepo, bastRepo, teamRepo, crmGW).
		WithBilling(fieldBillingGW).
		WithReschedule(rescheduleRepo).
		WithUploads(uploadsGW).
		WithRadius(radiusReader).
		WithActivation(activator).
		WithBranchSLAResolver(branchResolver).
		WithAddressResolver(branchResolver).
		WithTeamLeaderLookup(branchResolver).
		WithAudit(fieldAuditW).
		WithNotifier(fieldNotifier).
		WithServiceSchemaResolver(fieldServiceSchemaResolver)

	handler := fieldhttp.NewHandler(svc, verifier)
	uploadHandler := uploadshttp.NewHandler(uploadSvc, localStore, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "field-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("field-svc", pool.Ping)
	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	handler.Mount(server.Router)
	uploadHandler.Mount(server.Router)

	// Phase 2 — CS tickets + maintenance events. Same delivery shape
	// as crm-phase2: direct pgxpool, single file. Hexagonal layering
	// can come later once volumes justify it.
	fieldhttp.NewPhase2Handler(pool, verifier).Mount(server.Router)

	// Wave 65 — Operations module (Phase 1A closure).
	// Bulk ops + announcements + calendar + SLA dashboard + War Room hook.
	opshttp.NewHandler(pool, verifier).Mount(server.Router)

	// M5 r3 — kick off the SLA-breach watcher.
	go svc.StartSLAWatcher(ctx, 5*time.Minute, log)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("field-svc stopped")
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}
