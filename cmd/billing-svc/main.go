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
	"os"
	"os/signal"
	"syscall"
	"time"

	billingcrm "github.com/ion-core/backend/internal/billing/adapter/crm"
	billingfield "github.com/ion-core/backend/internal/billing/adapter/field"
	billinghttp "github.com/ion-core/backend/internal/billing/adapter/http"
	billingnet "github.com/ion-core/backend/internal/billing/adapter/network"
	billingpg "github.com/ion-core/backend/internal/billing/adapter/postgres"
	billingusecase "github.com/ion-core/backend/internal/billing/usecase"
	networkradius "github.com/ion-core/backend/internal/network/adapter/radius"
	platformpg "github.com/ion-core/backend/internal/platform/adapter/postgres"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
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
	radiusClient := networkradius.NewLocalClient(pool, log)
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
	platformSvc := platformusecase.NewService(platformSchemaRepo, platformOverrideRepo)
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
	server := httpserver.New(httpserver.DefaultConfig(cfg.HTTPPort), log)
	server.SetHealth("billing-svc", pool.Ping)
	handler.Mount(server.Router)
	priorityHandler.Mount(server.Router)

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

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("billing-svc stopped")
}
