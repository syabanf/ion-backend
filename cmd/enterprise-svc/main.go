// enterprise-svc — Enterprise CPQ MVP service.
//
// Phase 2 scope: Pricebook + Pricebook Lines + Opportunity. Future
// phases extend this binary with BOQ (Phase 3), Quotation +
// Negotiation (Phase 4), and Finance + EWO (Phase 5).
//
// Isolated bounded context: this service speaks only to its own
// `enterprise.*` schema. It does NOT cross-import from internal/crm,
// internal/warehouse, etc. Cross-context references (customer_id,
// owner_user_id, branch_id, allowed_provider_company_ids) are stored
// as plain UUIDs and resolved by the frontend at display time. This
// keeps the future extraction into a standalone microservice trivial.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	enterprisehttp "github.com/ion-core/backend/internal/enterprise/adapter/http"
	enterprisepg "github.com/ion-core/backend/internal/enterprise/adapter/postgres"
	enterprisetax "github.com/ion-core/backend/internal/enterprise/adapter/tax"
	enterprisecron "github.com/ion-core/backend/internal/enterprise/cron"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	enterpriseusecase "github.com/ion-core/backend/internal/enterprise/usecase"
	// Wave 93 — tax bounded context. Loose-coupled to enterprise.* (no
	// cross-schema FKs); mounted here only because we already have the
	// pool + auth verifier wired and a dedicated tax-svc binary would
	// be over-engineering for a Phase 1 scaffold.
	taxdjp "github.com/ion-core/backend/internal/tax/adapter/djp"
	taxhttp "github.com/ion-core/backend/internal/tax/adapter/http"
	taxpg "github.com/ion-core/backend/internal/tax/adapter/postgres"
	taxusecase "github.com/ion-core/backend/internal/tax/usecase"
	// Wave 107 — vendor metrics updater seam (cross-context). The
	// postgres ProviderRepository implements VendorMetricsUpdater; we
	// pass it via WithVendorMetrics so the IC-PO-accept hook can bump
	// provider counters. Vendor schema may not be applied; the seam is
	// nil-safe at the usecase layer.
	vendormgmtpg "github.com/ion-core/backend/internal/vendormgmt/adapter/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/notifyx"
)

func main() {
	cfg, err := config.Load("ENTERPRISE_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "enterprise-svc")
	log.Info("starting enterprise-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	pbRepo := enterprisepg.NewPricebookRepository(pool)
	plRepo := enterprisepg.NewPricebookLineRepository(pool)
	opRepo := enterprisepg.NewOpportunityRepository(pool)
	// Phase 3 — BOQ + approval + SLA repos.
	slaRepo := enterprisepg.NewSLATemplateRepository(pool)
	aptRepo := enterprisepg.NewApprovalTemplateRepository(pool)
	apiRepo := enterprisepg.NewApprovalInstanceRepository(pool)
	boqRepo := enterprisepg.NewBOQRepository(pool)
	boqLineRepo := enterprisepg.NewBOQLineRepository(pool)
	// Phase 4a — Quotation repo.
	quotationRepo := enterprisepg.NewQuotationRepository(pool)
	// Phase 4b — Negotiation repos.
	negCfgRepo := enterprisepg.NewNegotiationConfigRepository(pool)
	negRepo := enterprisepg.NewNegotiationRepository(pool)
	negRoundRepo := enterprisepg.NewNegotiationRoundRepository(pool)
	negApprovalRepo := enterprisepg.NewNegotiationRoundApprovalRepository(pool)
	// Phase 5 — Finance + EWO repos.
	invoiceRepo := enterprisepg.NewInvoiceRepository(pool)
	paymentRepo := enterprisepg.NewInvoicePaymentRepository(pool)
	ewoRepo := enterprisepg.NewEWORepository(pool)
	// Pre-launch (0030) — notifications.
	notificationRepo := enterprisepg.NewNotificationRepository(pool)
	// 0032 — internal_transactions ledger + audit writer.
	internalTxRepo := enterprisepg.NewInternalTransactionRepository(pool)
	auditWriter := auditpg.NewWriter(pool)
	// Pre-launch — termin / PO / proof / checklist / projects / RFQ.
	invoicePlanRepo := enterprisepg.NewInvoicePlanRepository(pool)
	poDocRepo := enterprisepg.NewPODocumentRepository(pool)
	paymentProofRepo := enterprisepg.NewPaymentProofRepository(pool)
	ewoChecklistRepo := enterprisepg.NewEWOChecklistRepository(pool)
	projectRepo := enterprisepg.NewProjectRepository(pool)
	projectSiteRepo := enterprisepg.NewProjectSiteRepository(pool)
	enterpriseSvcRepo := enterprisepg.NewEnterpriseServiceRepository(pool)
	rfqRepo := enterprisepg.NewRFQRepository(pool)
	// 0032 polish — notification prefs + EWO checklist templates.
	notificationPrefRepo := enterprisepg.NewNotificationPrefRepository(pool)
	ewoChecklistTemplateRepo := enterprisepg.NewEWOChecklistTemplateRepository(pool)
	// Wave 95 — Customer PO + Intercompany PO + IC pair config.
	customerPORepo := enterprisepg.NewCustomerPORepository(pool)
	icPORepo := enterprisepg.NewIntercompanyPORepository(pool)
	icPairRepo := enterprisepg.NewIntercompanyPairRepository(pool)
	// Wave 106 — Pre-BOQ required-field config repo (TC-OP-009).
	preBOQFieldRepo := enterprisepg.NewPreBOQRequiredFieldRepository(pool)
	// Wave 96 — EWO reschedule audit trail (dual-EWO scheduling).
	ewoScheduleHistoryRepo := enterprisepg.NewEWOScheduleHistoryRepository(pool)
	// Wave 103 — Technician mobile API repos. EWOMobileRepository
	// enforces side='y' + assigned_technician_user_id in SQL so no
	// caller can accidentally widen the technician scope.
	ewoMobileRepo := enterprisepg.NewEWOMobileRepository(pool)
	ewoPushLogRepo := enterprisepg.NewEWOPushLogRepository(pool)
	ewoChecklistProgressRepo := enterprisepg.NewEWOChecklistProgressRepository(pool)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	svc := enterpriseusecase.NewService(pbRepo, plRepo, opRepo, log).
		WithBOQ(slaRepo, aptRepo, apiRepo, boqRepo, boqLineRepo).
		WithQuotations(quotationRepo).
		WithNegotiation(negCfgRepo, negRepo, negRoundRepo, negApprovalRepo).
		WithFinance(invoiceRepo, paymentRepo, ewoRepo).
		WithNotifications(notificationRepo).
		WithAudit(auditWriter).
		WithInternalTransactions(internalTxRepo).
		WithInvoicePlans(invoicePlanRepo).
		WithPODocuments(poDocRepo).
		WithPaymentProofs(paymentProofRepo).
		WithEWOChecklist(ewoChecklistRepo).
		WithProjects(projectRepo, projectSiteRepo, enterpriseSvcRepo).
		WithRFQs(rfqRepo).
		WithNotificationPrefs(notificationPrefRepo).
		WithEWOChecklistTemplates(ewoChecklistTemplateRepo).
		WithCustomerPOs(customerPORepo).
		WithIntercompanyPOs(icPORepo, icPairRepo).
		WithEWOScheduling(ewoScheduleHistoryRepo).
		// Wave 106 — Pre-BOQ structured validator config + approval
		// advisory-lock pool.
		WithPreBOQRequiredFields(preBOQFieldRepo).
		WithLockPool(pool).
		// Wave 107 — vendor metrics updater seam. The postgres provider
		// repo's IncrementCompletedJob satisfies the VendorMetricsUpdater
		// port; the IC-PO-accept hook uses it to bump per-provider
		// counters. nil-safe — when the vendor schema isn't applied the
		// hook silently no-ops.
		WithVendorMetrics(vendormgmtpg.NewProviderRepository(pool))

	// Phase 2 handler mounts /pricebooks, /opportunities, etc.
	handler := enterprisehttp.NewHandler(svc, verifier)
	// Phase 3 handler mounts /boqs, /approval-templates, /sla-templates,
	// /approval-instances. The two handlers share the same chi.Router
	// so a single auth/cors middleware chain applies to all routes.
	boqHandler := enterprisehttp.NewBOQHandler(svc, verifier)
	// Phase 4a handler mounts /quotations + /quotations/{id}/pdf.
	quotationHandler := enterprisehttp.NewQuotationHandler(svc, verifier)
	// Phase 4b handler mounts negotiation config + lifecycle + rounds.
	negotiationHandler := enterprisehttp.NewNegotiationHandler(svc, verifier)
	// Phase 5 handler mounts /invoices + /ewos + payment routes.
	financeHandler := enterprisehttp.NewFinanceHandler(svc, verifier)
	// Pre-launch — notifications inbox.
	notificationHandler := enterprisehttp.NewNotificationHandler(svc, verifier)
	// Pre-launch — E5/E7/E8/E9/E11/E12 bundle.
	preLaunchHandler := enterprisehttp.NewPreLaunchHandler(svc, svc, verifier)
	// Phase 2 gap closure — service catalog + project milestones + S-Curve.
	phase2Handler := enterprisehttp.NewPhase2Handler(pool, verifier)

	// Wave 92 — multi-company holding scaffolding (read-only for now).
	// Mutating endpoints come once the FK rollout to existing enterprise
	// tables is agreed; today these surface holding_companies +
	// subsidiaries so the dashboard can pick a commercial owner without
	// guessing UUIDs. The handler depends directly on the two repos
	// rather than going through `port.UseCase` because Wave 92 carries
	// no business rules yet — see holding_handler.go header comment.
	holdingRepo := enterprisepg.NewHoldingCompanyRepository(pool)
	subsidiaryRepo := enterprisepg.NewSubsidiaryRepository(pool)
	holdingHandler := enterprisehttp.NewHoldingHandler(holdingRepo, subsidiaryRepo, verifier)

	// Wave 93 — tax bounded context (PKP / Faktur Pajak scaffold).
	// Wave 101 — DJP gateway is now the production-shaped HTTP client.
	// When DJP_ENABLED is not "true" it short-circuits to the same 503
	// djp.scaffold the Wave 93 stub returned, so deployments without
	// real DJP credentials keep working unchanged. Logs at startup
	// which mode is active.
	taxProfileRepo := taxpg.NewCompanyTaxProfileRepository(pool)
	taxFakturRepo := taxpg.NewFakturPajakRepository(pool)
	djpClient := taxdjp.NewClient(taxdjp.ConfigFromEnv())
	taxSvc := taxusecase.NewService(taxProfileRepo, taxFakturRepo, djpClient, log)
	taxHandler := taxhttp.NewHandler(taxSvc, verifier)

	// Wave 101 — bridge tax → enterprise so BOQ approval can stamp the
	// active tax_profile + snapshot hash. This is the ONE approved
	// cross-context reference: the resolver adapter wraps
	// tax.usecase.Service.GetActiveProfile and maps to the enterprise
	// port DTO.
	svc = svc.WithTaxResolver(enterprisetax.NewResolver(taxSvc))

	// Wave 95 — Customer PO + Intercompany PO HTTP surfaces.
	customerPOHandler := enterprisehttp.NewCustomerPOHandler(svc, verifier)
	icPOHandler := enterprisehttp.NewIntercompanyPOHandler(svc, verifier)
	// Wave 96 — TL Scheduling routes (/ewos/{id}/schedule etc.).
	tlSchedulingHandler := enterprisehttp.NewTLSchedulingHandler(svc, verifier)
	// Wave 103 — Technician mobile service + handler. The service is
	// constructed standalone (not via enterpriseusecase.Service builders)
	// because the mobile surface is a future split candidate; keeping
	// the deps narrow makes the move mechanical.
	mobileSvc := enterpriseusecase.NewTechnicianMobileService(
		ewoMobileRepo, ewoPushLogRepo, ewoChecklistProgressRepo, log,
	).WithAudit(auditWriter)
	mobileHandler := enterprisehttp.NewMobileEWOHandler(mobileSvc, verifier)

	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("enterprise-svc", pool.Ping)

	// Wave 97 — block suspended actors before any enterprise handler
	// runs. The middleware is a no-op for unauthenticated routes
	// (/healthz, etc.) because it short-circuits when no claims are
	// attached, so it's safe to mount at the router level. We sit it
	// BEFORE the handler mounts so all parallel route additions
	// (Wave 95 customer_po + IC-PO, future waves) inherit the check
	// automatically without per-handler wiring.
	server.Router.Use(enterprisehttp.RequireActiveActor(verifier))

	// Wave 105 — Prometheus instrumentation. Placed after the
	// authz middleware so the in-flight gauge / latency histogram
	// reflect actually-served requests (suspended actors short-
	// circuit upstream and don't reach the handler). /metrics is
	// mounted next to /healthz — scraped by ops Prometheus.
	server.Router.Use(httpserver.PrometheusMiddleware("enterprise-svc"))
	server.Router.Handle("/metrics", httpserver.MetricsHandler())

	handler.Mount(server.Router)
	boqHandler.Mount(server.Router)
	quotationHandler.Mount(server.Router)
	negotiationHandler.Mount(server.Router)
	financeHandler.Mount(server.Router)
	notificationHandler.Mount(server.Router)
	preLaunchHandler.Mount(server.Router)
	phase2Handler.Mount(server.Router)
	holdingHandler.Mount(server.Router)
	taxHandler.Mount(server.Router)
	customerPOHandler.Mount(server.Router)
	icPOHandler.Mount(server.Router)
	tlSchedulingHandler.Mount(server.Router)
	mobileHandler.Mount(server.Router)

	// Push-notification dispatcher. Stub provider until FCM credentials
	// land (docs/backlog.md §FCM/APNS). The milestone-invoicer fans
	// push messages alongside the persisted inbox row.
	notifier := notifyx.New(pool, log)
	log.Info("notifyx dispatcher wired", "provider", "stub")

	// Periodic workers — milestone-invoicing trigger + vendor metrics
	// derivation. Both are idempotent + log-only on failure, so a
	// startup blip doesn't cascade. We wire the Service as the termin
	// issuer so milestones hitting 100% auto-create their termin
	// invoice (vs. just notifying finance).
	enterprisecron.New(pool, log).
		WithTerminIssuer(svc).
		WithNotifier(notifier).
		WithAuditWriter(auditWriter).
		WithTechnicianPushDispatcher(ewoMobileRepo, ewoPushLogRepo, notifier).
		// Wave 106 — opportunity auto-Lost watcher + quotation expiry.
		WithAutoLostSweeper(svc).
		WithQuotationExpirer(svc).
		// Wave 107 — invoice reminder dispatcher. Daily ticker; fires
		// notifyx push for invoices coming due in <= 3 days and stamps
		// reminder_sent_at so the dedupe sticks.
		WithInvoiceReminderDispatcher(svc).
		Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("enterprise-svc stopped")
}
