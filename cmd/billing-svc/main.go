// billing-svc — Billing & Finance service.
//
// Round-1 scope: invoices (OTC + recurring + excess + addon), line items,
// manual payment recording. The single load-bearing cross-cut is the
// payment-gates-NOC rule, exposed via IsOrderOTCPaid and called by field-svc
// during BAST verify.
//
// Round 2 (this binary): recurring/anniversary billing scheduler,
// late fees, auto-suspend/restore, customer status state machine,
// commission calculation on first payment (5-party split).
//
// Round 3 (not yet wired): Xendit gateway + webhook, DJP e-Faktur,
// WhatsApp/email reminders, voluntary termination, referral reward.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	billingcrm "github.com/ion-core/backend/internal/billing/adapter/crm"
	billingfield "github.com/ion-core/backend/internal/billing/adapter/field"
	billinghttp "github.com/ion-core/backend/internal/billing/adapter/http"
	billingnet "github.com/ion-core/backend/internal/billing/adapter/network"
	billingpg "github.com/ion-core/backend/internal/billing/adapter/postgres"
	billingcron "github.com/ion-core/backend/internal/billing/cron"
	billingdomain "github.com/ion-core/backend/internal/billing/domain"
	billingport "github.com/ion-core/backend/internal/billing/port"
	billingusecase "github.com/ion-core/backend/internal/billing/usecase"
	networkradius "github.com/ion-core/backend/internal/network/adapter/radius"
	platformcrm "github.com/ion-core/backend/internal/platform/adapter/crm"
	platformpg "github.com/ion-core/backend/internal/platform/adapter/postgres"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/notifyx"
)

func main() {
	cfg, err := config.Load("BILLING_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "billing-svc")
	log.Info("starting billing-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	invoiceRepo := billingpg.NewInvoiceRepository(pool)
	paymentRepo := billingpg.NewPaymentRepository(pool)

	// M6 r2 — new repos + gateways.
	policyRepo := billingpg.NewPolicyRepository(pool)
	cycleRepo := billingpg.NewCycleRepository(pool)
	commissionRepo := billingpg.NewCommissionRepository(pool)
	crmGW := billingcrm.New(pool)
	// In-process RADIUS client for suspend/restore. Round-4 swaps to HTTP.
	radiusClient := networkradius.NewLocalClient(pool, log).WithAudit(auditpg.NewWriter(pool))
	netGW := billingnet.New(radiusClient)

	// M6 r3 — termination + referral repos + field gateway.
	terminationRepo := billingpg.NewTerminationRepository(pool)
	rewardRepo := billingpg.NewReferralRewardRepository(pool)
	fieldGW := billingfield.New(pool)

	// M6 r3 — customer-portal OTP for self-service termination.
	otpRepo := billingpg.NewCustomerOTPRepository(pool)
	// The crmGW satisfies both CRMGateway and CustomerLookupGateway.
	includeDevOTP := os.Getenv("PORTAL_DEV_OTP") == "true"

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	// Platform Schema System v1 resolver. Built against the same pool
	// (single DB today). When platform splits out, swap to an HTTP
	// client implementing port.SchemaResolver; nothing in the billing
	// path needs to change.
	platformSchemaRepo := platformpg.NewSchemaRepository(pool)
	platformOverrideRepo := platformpg.NewOverrideRepository(pool)
	// Wave 82 Tier 2c — auto-load customer schema version lock so every
	// dunning/recurring tick honors the Wave 80b snapshot.
	platformSvc := platformusecase.NewService(platformSchemaRepo, platformOverrideRepo).
		WithCustomerLockReader(platformcrm.NewLockReader(pool))
	schemaResolver := platformusecase.NewResolver(platformSvc)

	svc := billingusecase.NewService(invoiceRepo, paymentRepo).
		WithR2(policyRepo, cycleRepo, commissionRepo, crmGW, netGW, log).
		WithR3(terminationRepo, rewardRepo, fieldGW).
		WithPortal(otpRepo, crmGW, includeDevOTP).
		WithSchemaResolver(schemaResolver)

	// Tunes: burst=4 so a customer can OTP-request, type wrong, request
	// again, and confirm — all within the burst. Refill=0.05/s (one
	// token every 20s) keeps brute-force burns very expensive.
	portalRL := httpserver.NewRateLimit(4, 0.05)
	handler := billinghttp.NewHandler(svc, verifier).WithPortalRateLimit(portalRL)
	priorityHandler := billinghttp.NewPriorityHandler(pool, verifier)

	// Wave 115 — Add-On Billing handler. The CRM-side /portal/addons/buy
	// path remains the authoritative purchase write; this handler adds
	// the parallel /portal/billing/add-ons/* surface + an admin read.
	addonRepo := billingpg.NewAddOnPurchaseRepository(pool)
	catalogReader := billingpg.NewCatalogReader(pool)
	addonCRMSync := billingpg.NewAddOnCRMGateway(pool)
	addonSvc := billingusecase.NewAddOnService(addonRepo, catalogReader, addonCRMSync)
	addonHandler := billinghttp.NewAddOnHandler(addonSvc, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "billing-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("billing-svc", pool.Ping)
	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	handler.Mount(server.Router)
	priorityHandler.Mount(server.Router)
	addonHandler.Mount(server.Router)

	// M6 r2 — background scheduler. The ticker fires every 30 minutes;
	// each pass is idempotent so the cadence is just about how soon we
	// react. Round-4 will move this out-of-process so it can run on
	// only one replica via a leader-election lock.
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		// One immediate tick on startup so cold-starts don't wait.
		if _, err := svc.RunBillingTick(ctx, time.Now().UTC()); err != nil {
			log.Error("initial billing tick failed", "err", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := svc.RunBillingTick(ctx, time.Now().UTC()); err != nil {
					log.Error("billing tick failed", "err", err)
				}
			}
		}
	}()

	// =====================================================================
	// Wave 114 — Billing orchestration crons.
	//
	// Five evaluators (reminder, late-fee, suspension, restore-on-paid,
	// commission-trigger) drive the audit's "highest leverage-per-effort"
	// gap. Each is nil-safe: cmd boots even when a bridge isn't fully
	// wired yet (the orchestration service logs a TODO and the cron tick
	// no-ops). The four log tables (billing.reminder_log,
	// billing.late_fee_applications, billing.suspension_actions,
	// billing.commission_triggers) shipped in migration 0077.
	// =====================================================================
	auditWriter := auditpg.NewWriter(pool)
	reminderLogRepo := billingpg.NewReminderLogRepository(pool)
	lateFeeAppRepo := billingpg.NewLateFeeApplicationRepository(pool)
	suspensionActionRepo := billingpg.NewSuspensionActionRepository(pool)
	commissionTriggerRepo := billingpg.NewCommissionTriggerRepository(pool)
	planChangeReader := billingpg.NewPlanChangeReader(pool)
	customerReader := billingpg.NewCustomerReader(pool)

	// Bridges: the radius restorer wraps the in-process RADIUS client we
	// already built for the legacy r2 suspend/restore loop. The
	// CustomerSuspender wraps the same CRM gateway used for billing's
	// suspend/restore path. Both are tight adapters so Wave 114's
	// cron loop doesn't need its own DB query path.
	radiusBridge := newRadiusRestorerBridge(radiusClient)
	suspenderBridge := newCustomerSuspenderBridge(crmGW)
	reminderDispatcher := newReminderDispatcherBridge(notifyx.New(pool, log), log)

	orchestrator := billingusecase.NewOrchestrationService(
		invoiceRepo,
		reminderLogRepo,
		lateFeeAppRepo,
		suspensionActionRepo,
		commissionTriggerRepo,
		planChangeReader,
		customerReader,
		schemaResolver,
		radiusBridge,
		suspenderBridge,
		reminderDispatcher,
		auditWriter,
		log,
	)
	billingcron.New(pool, log).
		WithReminderEvaluator(orchestrator).
		WithLateFeeApplier(orchestrator).
		WithSuspensionEvaluator(orchestrator).
		WithRadiusRestoreOnPaid(orchestrator).
		WithCommissionTriggerEvaluator(orchestrator).
		Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("billing-svc stopped")
}

// =====================================================================
// Wave 114 — Cross-context bridges.
//
// Each bridge is a small struct implementing a billing/port interface
// by delegating to an existing adapter. Keeps the orchestration usecase
// agnostic to which RADIUS / CRM / notifyx adapter we wire in any given
// deployment.
// =====================================================================

// radiusRestorerBridge implements billingport.RADIUSRestorer by
// forwarding to the in-process networkradius.LocalRadiusClient that
// the legacy r2 suspend/restore path already uses. When the network
// context splits out to its own service, this swaps to an HTTP-backed
// implementation.
type radiusRestorerBridge struct {
	client *networkradius.LocalRadiusClient
}

func newRadiusRestorerBridge(client *networkradius.LocalRadiusClient) *radiusRestorerBridge {
	return &radiusRestorerBridge{client: client}
}

var _ billingport.RADIUSRestorer = (*radiusRestorerBridge)(nil)

func (b *radiusRestorerBridge) RestoreCustomer(ctx context.Context, customerID uuid.UUID) error {
	if b == nil || b.client == nil {
		return nil
	}
	_, err := b.client.Restore(ctx, customerID)
	return err
}

// customerSuspenderBridge implements billingport.CustomerSuspender by
// delegating to the existing billing.adapter.crm.Gateway's
// SetCustomerStatus. Maps the new CustomerSuspensionState enum back
// to the legacy CRM status string the gateway already speaks
// ('active' / 'suspended').
type customerSuspenderBridge struct {
	crm *billingcrm.Gateway
}

func newCustomerSuspenderBridge(crm *billingcrm.Gateway) *customerSuspenderBridge {
	return &customerSuspenderBridge{crm: crm}
}

var _ billingport.CustomerSuspender = (*customerSuspenderBridge)(nil)

func (b *customerSuspenderBridge) SetSuspensionState(ctx context.Context, customerID uuid.UUID, state billingdomain.CustomerSuspensionState) error {
	if b == nil || b.crm == nil {
		return nil
	}
	switch state {
	case billingdomain.CustomerSuspensionStateActive:
		return b.crm.SetCustomerStatus(ctx, customerID, "active", "wave114.cron.restore")
	case billingdomain.CustomerSuspensionStateSoftSuspend:
		return b.crm.SetCustomerStatus(ctx, customerID, "suspended", "wave114.cron.soft_suspend")
	case billingdomain.CustomerSuspensionStateHardSuspend:
		// Legacy CRM has a single 'suspended' status; the hard/soft
		// distinction lives on billing.suspension_actions for now. A
		// future wave can split the CRM enum.
		return b.crm.SetCustomerStatus(ctx, customerID, "suspended", "wave114.cron.hard_suspend")
	}
	return nil
}

// reminderDispatcherBridge implements billingport.ReminderDispatcher
// by wrapping notifyx. It's a minimal adapter — the WhatsApp /
// template-driven dispatcher path is the responsibility of a follow-
// up wave (notifyx today ships only a stub provider, so the cron
// effectively logs every reminder). The bridge still writes the row
// to billing.reminder_log so the dedupe is correct when WhatsApp
// lands.
type reminderDispatcherBridge struct {
	notifier *notifyx.Dispatcher
	log      *slog.Logger
}

func newReminderDispatcherBridge(notifier *notifyx.Dispatcher, log *slog.Logger) *reminderDispatcherBridge {
	return &reminderDispatcherBridge{notifier: notifier, log: log.With("bridge", "reminder_dispatch")}
}

var _ billingport.ReminderDispatcher = (*reminderDispatcherBridge)(nil)

func (b *reminderDispatcherBridge) SendReminder(
	ctx context.Context,
	target billingport.ReminderTarget,
	invoice billingport.ReminderInvoiceSnapshot,
	kind billingdomain.ReminderKind,
	channel string,
) (string, error) {
	if b == nil || b.notifier == nil {
		return "", nil
	}
	msg := notifyx.Message{
		Title:    "Invoice " + invoice.InvoiceNumber + " — " + string(kind),
		Body:     "Invoice " + invoice.InvoiceNumber + " is due " + invoice.DueDate.Format("2006-01-02"),
		DeepLink: "/billing/invoices/" + invoice.InvoiceID.String(),
		Topic:    "billing.reminder",
		Data: map[string]string{
			"invoice_id":      invoice.InvoiceID.String(),
			"invoice_number":  invoice.InvoiceNumber,
			"kind":            string(kind),
			"channel":         channel,
			"due_date":        invoice.DueDate.Format("2006-01-02"),
		},
	}
	if target.CustomerID != uuid.Nil {
		b.notifier.Send(ctx, notifyx.Target{CustomerID: target.CustomerID}, msg)
	}
	// notifyx.Send doesn't surface a message-id yet; we return "" and
	// let the orchestration service stamp delivered=true (since Send
	// is fire-and-forget).
	return "", nil
}

