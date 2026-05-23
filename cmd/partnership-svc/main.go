// partnership-svc — Partnership bounded context service.
//
// Wave 100 scope: Partnership Monthly Submission + Settlement + Monthly
// Compliance Check. Isolated bounded context: this service speaks only
// to its own `partnership.*` schema. It does NOT cross-import from
// internal/reseller, internal/enterprise, internal/tax. Cross-context
// UUIDs (reseller_account_id, signed_by, submitted_by) are stored as
// plain UUIDs and resolved by the calling service at display time.
// This keeps the future extraction into a standalone microservice
// trivial (same playbook as reseller-svc).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	partnershiphttp "github.com/ion-core/backend/internal/partnership/adapter/http"
	partnershippg "github.com/ion-core/backend/internal/partnership/adapter/postgres"
	partnershipcron "github.com/ion-core/backend/internal/partnership/cron"
	partnershipusecase "github.com/ion-core/backend/internal/partnership/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/notifyx"
)

func main() {
	cfg, err := config.Load("PARTNERSHIP_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "partnership-svc")
	log.Info("starting partnership-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories — one per aggregate, all bound to the `partnership`
	// schema. Cross-repo composition happens in the usecase services.
	agreementRepo := partnershippg.NewAgreementRepository(pool)
	submissionRepo := partnershippg.NewMonthlySubmissionRepository(pool)
	settlementRepo := partnershippg.NewSettlementRepository(pool)
	complianceRepo := partnershippg.NewComplianceEvaluationRepository(pool)

	// Stub side-effect adapters — local-disk evidence store + text-
	// based settlement PDF stub. Wave 100b swaps these for S3 / real
	// PDF without touching the usecase.
	evidenceStore := partnershippg.NewLocalEvidenceStore("")
	pdfGen := partnershippg.NewStubSettlementPDFGenerator()

	// Audit writer shared by every service in this binary.
	auditWriter := auditpg.NewWriter(pool)

	// Notifyx dispatcher — used by ComplianceService for breach
	// notifications. Default provider is the stub (logs only).
	notifier := notifyx.New(pool, log)

	// Usecases — agreement, settlement (composed into submission for
	// the confirm → issue chain), submission, compliance.
	agreementSvc := partnershipusecase.NewAgreementService(agreementRepo, auditWriter)
	settlementSvc := partnershipusecase.NewSettlementService(
		settlementRepo, submissionRepo, agreementRepo,
		evidenceStore, pdfGen,
		auditWriter, log,
	)
	submissionSvc := partnershipusecase.NewSubmissionService(
		submissionRepo, agreementRepo, settlementSvc,
		auditWriter,
	)
	complianceSvc := partnershipusecase.NewComplianceService(
		complianceRepo, submissionRepo, agreementRepo,
		notifier, auditWriter, log,
	)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	handler := partnershiphttp.NewHandler(
		agreementSvc, submissionSvc, settlementSvc, complianceSvc,
		verifier,
	)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "partnership-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("partnership-svc", pool.Ping)
	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	handler.Mount(server.Router)

	// Compliance evaluator cron — ticks daily. Idempotent via the
	// UNIQUE (reseller_account_id, period_year, period_month)
	// constraint on partnership.compliance_evaluations: re-running on
	// the same period is a no-op. Same in-process pattern as
	// warehouse-svc's Wave 88b cascade cron (cmd/warehouse-svc/main.go).
	evaluator := partnershipcron.NewMonthlyComplianceEvaluator(complianceSvc, log)
	go partnershipcron.RunDaily(ctx, evaluator)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("partnership-svc stopped")
}
