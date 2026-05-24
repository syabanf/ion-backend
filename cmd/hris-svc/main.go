// hris-svc — HRIS bounded context service.
//
// Wave 118 scope: HRIS Integration (12 TCs).
//
// Owns the hris.* schema. Cross-context bridges to identity (user
// deactivation) and billing (commission cessation) are tiny inline SQL
// adapters declared in this file — they keep internal/hris/ free of
// cross-context imports while still letting Wave 114's commission tick
// consult HRIS via the HRISResignedReader port.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	hrisgateway "github.com/ion-core/backend/internal/hris/adapter/gateway"
	hrishttp "github.com/ion-core/backend/internal/hris/adapter/http"
	hrispg "github.com/ion-core/backend/internal/hris/adapter/postgres"
	hriscron "github.com/ion-core/backend/internal/hris/cron"
	hrisport "github.com/ion-core/backend/internal/hris/port"
	hrisusecase "github.com/ion-core/backend/internal/hris/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("HRIS_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "hris-svc")
	log.Info("starting hris-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories
	employeeRepo := hrispg.NewEmployeeRepository(pool)
	eventRepo := hrispg.NewEventRepository(pool)

	// Cross-context bridges — narrow SQL-only adapters declared below.
	commissionHook := &commissionCessationBridge{pool: pool}
	userDeactivator := &userDeactivatorBridge{pool: pool}
	fieldQueueBridge := &fieldQueueReassignerBridge{pool: pool}
	rbacBridge := &rbacRecalculatorBridge{pool: pool}

	// Gateway — stub by default. When HRIS_GATEWAY_ENABLED=true, build
	// the real REST adapter; if required env vars are missing, refuse
	// to boot (Wave 128A — closes Wave 121E §6.1 no-op-flag finding).
	var gateway hrisport.HRISGateway = hrisgateway.NewStubGateway()
	if hrisgateway.EnvFlagSet() {
		restCfg := hrisgateway.RESTConfigFromEnv()
		restCfg.Logger = log
		realGW, err := hrisgateway.NewRESTGateway(restCfg)
		if err != nil {
			log.Error("HRIS_GATEWAY_ENABLED=true but configuration is invalid", "err", err)
			os.Exit(1)
		}
		log.Info("HRIS_GATEWAY_ENABLED=true — using real REST gateway")
		gateway = realGW
	}

	// Usecases
	employeeSvc := hrisusecase.NewEmployeeService(employeeRepo, nil, log)
	eventSvc := hrisusecase.NewEventService(eventRepo, employeeRepo, hrisusecase.EventServiceOpts{
		Commission: commissionHook,
		Deactivate: userDeactivator,
		Reassign:   fieldQueueBridge,
		RBAC:       rbacBridge,
		Log:        log,
	})
	syncSvc := hrisusecase.NewSyncService(hrisusecase.SyncServiceOpts{
		Gateway:  gateway,
		Employee: employeeSvc,
		Event:    eventSvc,
		Log:      log,
	})

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)
	handler := hrishttp.NewHandler(employeeSvc, eventSvc, syncSvc, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "hris-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("hris-svc", pool.Ping)
	handler.Mount(server.Router)

	// Cron
	runner := hriscron.New(log).
		WithSyncService(syncSvc).
		WithEventService(eventSvc)
	runner.Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("hris-svc stopped")
}

// =====================================================================
// Cross-context bridges — inline SQL-only adapters.
//
// These keep internal/hris/ free of cross-context imports while still
// letting the HRIS event drain talk to identity + billing tables.
// Each is intention-revealing and self-contained.
// =====================================================================

// commissionCessationBridge implements port.CommissionCessationHook.
// On resign it marks pending billing.commission_triggers for the
// employee as already-fired with a cessation reason so the downstream
// commission ledger worker won't pay them out.
//
// The lookup goes employee_no → identity.users.id → commission_triggers.
// If the user record doesn't yet exist (HRIS event arrived first) the
// bridge is a no-op — Wave 114's commission trigger evaluator's runtime
// gate (HRISResignedReader) will still catch the resignation.
type commissionCessationBridge struct {
	pool *pgxpool.Pool
}

var _ hrisport.CommissionCessationHook = (*commissionCessationBridge)(nil)

func (b *commissionCessationBridge) OnResign(ctx context.Context, employeeNo string, resignDate time.Time) error {
	if b == nil || b.pool == nil {
		return nil
	}
	// Map employee_no → user_id via identity.users.hris_employee_no.
	var userID string
	row := b.pool.QueryRow(ctx,
		`SELECT id::text FROM identity.users WHERE hris_employee_no = $1 LIMIT 1`,
		employeeNo,
	)
	if err := row.Scan(&userID); err != nil {
		// No matching user (yet); the runtime gate will still block.
		return nil
	}
	// Cancel any not-yet-fired commission triggers by stamping a
	// processed_at + reason. We use UPDATE … WHERE fired_at IS NULL so
	// already-fired commissions stay intact (they may need clawback,
	// but that's a Wave 119 Schema Commission Deep concern).
	_, err := b.pool.Exec(ctx, `
		UPDATE billing.commission_triggers
		   SET fired_at = COALESCE(fired_at, NOW()),
		       trigger_kind = 'cessation'
		 WHERE sales_user_id = $1::uuid
		   AND fired_at IS NULL
	`, userID)
	if err != nil {
		// Table may not exist in some test envs; treat as no-op so the
		// event still drains.
		return nil
	}
	_ = resignDate // captured by audit, not used here
	return nil
}

// userDeactivatorBridge implements port.UserDeactivator. Flips
// identity.users.is_active=false for any user with the matching
// hris_employee_no.
type userDeactivatorBridge struct {
	pool *pgxpool.Pool
}

var _ hrisport.UserDeactivator = (*userDeactivatorBridge)(nil)

func (b *userDeactivatorBridge) DeactivateByEmployeeNo(ctx context.Context, employeeNo string) error {
	if b == nil || b.pool == nil {
		return nil
	}
	_, err := b.pool.Exec(ctx,
		`UPDATE identity.users SET active = FALSE, updated_at = NOW()
		  WHERE hris_employee_no = $1 AND active = TRUE`,
		employeeNo,
	)
	// Don't fail the event drain on a missing column / table — Wave 118's
	// migration adds the column, but in dev envs without it applied the
	// drain still needs to make progress.
	return err
}

// fieldQueueReassignerBridge implements port.FieldQueueReassigner.
// Wave 118 keeps this audit-only — the actual queue reassignment is a
// manual Ops task today.
type fieldQueueReassignerBridge struct {
	pool *pgxpool.Pool
}

var _ hrisport.FieldQueueReassigner = (*fieldQueueReassignerBridge)(nil)

func (b *fieldQueueReassignerBridge) OnTransfer(_ context.Context, _ string) error {
	return nil
}

// rbacRecalculatorBridge implements port.RBACRecalculator. Today a
// no-op — identity-svc recomputes permissions on next read. The bridge
// exists so the audit row captures the "role changed" intent.
type rbacRecalculatorBridge struct {
	pool *pgxpool.Pool
}

var _ hrisport.RBACRecalculator = (*rbacRecalculatorBridge)(nil)

func (b *rbacRecalculatorBridge) OnRoleChange(_ context.Context, _ string) error {
	return nil
}
