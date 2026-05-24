// invoice-svc — Invoice Service bounded context binary.
//
// Wave 115 scope: dedicated read-heavy invoicesvc surface — issuance
// snapshots, credit-note state machine, bulk generation queue, customer
// + dashboard monitoring. Isolated bounded context: this service speaks
// only to its own `invoicesvc.*` schema plus SQL-only reads of
// `billing.invoices` (and `enterprise.invoices` when enabled).
//
// Cross-context UUIDs are stored as plain UUIDs and resolved at query
// time via the SQL InvoiceReader. NO Go imports from internal/billing
// or internal/enterprise — the future extraction into a standalone
// microservice stays trivial.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	invoicehttp "github.com/ion-core/backend/internal/invoicesvc/adapter/http"
	invoicepg "github.com/ion-core/backend/internal/invoicesvc/adapter/postgres"
	invoicecron "github.com/ion-core/backend/internal/invoicesvc/cron"
	invoiceusecase "github.com/ion-core/backend/internal/invoicesvc/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("INVOICE_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "invoice-svc")
	log.Info("starting invoice-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories.
	snapRepo := invoicepg.NewInvoiceSnapshotRepository(pool)
	cnRepo := invoicepg.NewCreditNoteRepository(pool)
	bulkJobRepo := invoicepg.NewBulkJobRepository(pool)
	bulkItemRepo := invoicepg.NewBulkItemRepository(pool)

	// SQL-only cross-context reader. Phase 1B is broadband-only, so we
	// leave the enterprise union OFF by default. Flip via env when
	// enterprise.invoices conforms to the projection columns.
	reader := invoicepg.NewInvoiceReader(pool)
	if os.Getenv("INVOICE_SVC_INCLUDE_ENTERPRISE") == "true" {
		reader = reader.WithEnterprise(true)
	}

	// Usecases. InvoiceGenerator stays nil — BulkService gracefully
	// fails queued items with a "not configured" reason. The
	// production hook lands when a billing-svc adapter ships.
	snapshotSvc := invoiceusecase.NewSnapshotService(snapRepo, reader)
	// Wave 128B — wire the invoice-ceiling validator (closes
	// TC-ISV-OVERISSUE). Create now refuses any amount that would push
	// cumulative non-voided credit past invoice.Total.
	cnSvc := invoiceusecase.NewCreditNoteServiceWithInvoices(cnRepo, reader)
	bulkSvc := invoiceusecase.NewBulkService(bulkJobRepo, bulkItemRepo, reader, nil)
	monitorSvc := invoiceusecase.NewMonitoringService(reader)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	handler := invoicehttp.NewHandler(snapshotSvc, cnSvc, bulkSvc, monitorSvc, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "invoice-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("invoice-svc", pool.Ping)
	handler.Mount(server.Router)

	// Cron.
	invoicecron.NewBulkJobRunner(bulkSvc, bulkJobRepo, log).Start(ctx)
	invoicecron.NewSnapshotBackfillScan(snapshotSvc, reader, log).Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("invoice-svc stopped")
}
