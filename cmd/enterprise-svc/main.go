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
	enterprisecron "github.com/ion-core/backend/internal/enterprise/cron"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	enterpriseusecase "github.com/ion-core/backend/internal/enterprise/usecase"
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
		WithEWOChecklistTemplates(ewoChecklistTemplateRepo)

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

	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("enterprise-svc", pool.Ping)
	handler.Mount(server.Router)
	boqHandler.Mount(server.Router)
	quotationHandler.Mount(server.Router)
	negotiationHandler.Mount(server.Router)
	financeHandler.Mount(server.Router)
	notificationHandler.Mount(server.Router)
	preLaunchHandler.Mount(server.Router)
	phase2Handler.Mount(server.Router)

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
		Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("enterprise-svc stopped")
}
