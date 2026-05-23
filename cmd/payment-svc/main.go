// payment-svc — Payment Service bounded context binary.
//
// Wave 111 scope: Payment Service Architecture + Routing + Webhook
// ingest + H2H bank reconciliation + Refund flow. Isolated bounded
// context: this service speaks only to its own `payment.*` schema.
// It does NOT cross-import from internal/billing, internal/crm,
// internal/enterprise. Cross-context UUIDs (invoice_id, customer_id)
// are stored as plain UUIDs and resolved by the calling service at
// display time. This keeps the future extraction into a fully
// standalone microservice trivial.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	paymentcron "github.com/ion-core/backend/internal/payment/cron"
	paymentgateway "github.com/ion-core/backend/internal/payment/adapter/gateway"
	paymenthttp "github.com/ion-core/backend/internal/payment/adapter/http"
	paymentpg "github.com/ion-core/backend/internal/payment/adapter/postgres"
	paymentusecase "github.com/ion-core/backend/internal/payment/usecase"
	auditpkg "github.com/ion-core/backend/pkg/audit"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("PAYMENT_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "payment-svc")
	log.Info("starting payment-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories — one per aggregate, all scoped to `payment.*`.
	gatewayRepo := paymentpg.NewPaymentGatewayRepository(pool)
	methodRepo := paymentpg.NewPaymentMethodRepository(pool)
	_ = methodRepo // reserved for the saved-method endpoints (Wave 112+)
	intentRepo := paymentpg.NewPaymentIntentRepository(pool)
	webhookRepo := paymentpg.NewPaymentWebhookRepository(pool)
	refundRepo := paymentpg.NewRefundRepository(pool)
	h2hRepo := paymentpg.NewH2HRepository(pool)

	// Gateway clients — stub-mode by default. Real REST clients land
	// in a future wave behind the XENDIT_ENABLED / BCA_H2H_ENABLED /
	// MIDTRANS_ENABLED env flags.
	gatewayRegistry := paymentgateway.NewStubRegistry(paymentgateway.SecretsFromEnv())

	// Routing policy.
	routing := paymentusecase.NewRoutingService()

	// Audit writer — Wave 105 chain-hash + cross-service writer. Falls
	// back to Nop if the audit schema isn't migrated yet.
	auditW := auditpkg.Writer(auditpg.NewWriter(pool))

	// Usecases.
	intentSvc := paymentusecase.NewIntentService(intentRepo, gatewayRepo, webhookRepo, routing, gatewayRegistry, auditW)
	webhookSvc := paymentusecase.NewWebhookService(webhookRepo, intentRepo, gatewayRepo, gatewayRegistry, auditW)
	refundSvc := paymentusecase.NewRefundService(refundRepo, intentRepo, gatewayRepo, gatewayRegistry, auditW)
	h2hSvc := paymentusecase.NewH2HService(h2hRepo, intentRepo, gatewayRepo, gatewayRegistry, auditW)
	gatewaySvc := paymentusecase.NewGatewayService(gatewayRepo)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	handler := paymenthttp.NewHandler(intentSvc, webhookSvc, refundSvc, h2hSvc, gatewaySvc, h2hSvc, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "payment-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("payment-svc", pool.Ping)
	handler.Mount(server.Router)

	// Cron — stale-intent expirer + H2H rematch.
	paymentcron.NewExpireStaleIntentsWorker(intentSvc, 0, log).Start(ctx)
	paymentcron.NewMatchPendingH2HStatementsWorker(h2hSvc, log).Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("payment-svc stopped")
}
