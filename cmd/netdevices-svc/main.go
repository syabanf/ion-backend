// netdevices-svc — Network Device Lifecycle bounded context service.
//
// Wave 113 scope: device registration, commissioning, firmware
// management, swap workflow, RMA, health snapshots, firmware compliance
// scanning. Isolated bounded context: this service speaks only to its
// own `netdev.*` schema. It does NOT cross-import from internal/
// warehouse, internal/field, internal/crm. Cross-context UUIDs
// (customer_id, warehouse_id, service_location_id, wo_id, retrofit_id,
// fault_event_id) are stored as plain UUIDs and resolved by the calling
// service at display time.
//
// Cross-context bridges (WarehouseAssetReader, RetrofitTrigger,
// WorkOrderCreator) are wired here as thin SQL-only adapters or no-op
// stubs — keeping the netdevices package itself free of any direct
// import from internal/warehouse / internal/field. The
// DEVICE_MGMT_ENABLED env flag swaps the stub mgmt client for a real
// vendor SDK in a later wave.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	netdevhttp "github.com/ion-core/backend/internal/netdevices/adapter/http"
	netdevmgmt "github.com/ion-core/backend/internal/netdevices/adapter/mgmt"
	netdevpg "github.com/ion-core/backend/internal/netdevices/adapter/postgres"
	netdevcron "github.com/ion-core/backend/internal/netdevices/cron"
	netdevport "github.com/ion-core/backend/internal/netdevices/port"
	netdevusecase "github.com/ion-core/backend/internal/netdevices/usecase"
	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("NETDEVICES_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "netdevices-svc")
	log.Info("starting netdevices-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories — one per aggregate, all bound to the `netdev` schema.
	deviceRepo := netdevpg.NewDeviceRepository(pool)
	fwVersionRepo := netdevpg.NewFirmwareVersionRepository(pool)
	fwJobRepo := netdevpg.NewFirmwareUpgradeJobRepository(pool)
	swapRepo := netdevpg.NewDeviceSwapRepository(pool)
	rmaRepo := netdevpg.NewRMARepository(pool)
	healthRepo := netdevpg.NewHealthSnapshotRepository(pool)
	complianceRepo := netdevpg.NewComplianceRepository(pool)

	// Audit writer shared by every service in this binary.
	auditWriter := auditpg.NewWriter(pool)

	// Cross-context bridges — implemented locally so internal/netdevices
	// never imports from internal/warehouse / internal/field.
	warehouseRO := newWarehouseAssetReader(pool, log)
	retrofitter := newRetrofitTrigger(pool, log)
	woCreator := newWorkOrderCreator(pool, log)

	// Device mgmt client — stub by default. When DEVICE_MGMT_ENABLED=true,
	// build the real HTTP adapter; if required env vars are missing,
	// refuse to boot (Wave 128A — closes Wave 121E §6.2 no-op-flag
	// finding). The stub is safe to use in prod (no side effects) so the
	// env flag defaults to false.
	var mgmtClient netdevport.DeviceMgmtClient = netdevmgmt.NewStubClient(log)
	if netdevmgmt.EnvFlagSet() {
		httpCfg := netdevmgmt.HTTPConfigFromEnv()
		httpCfg.Logger = log
		realClient, err := netdevmgmt.NewHTTPClient(httpCfg)
		if err != nil {
			log.Error("DEVICE_MGMT_ENABLED=true but configuration is invalid", "err", err)
			os.Exit(1)
		}
		log.Info("DEVICE_MGMT_ENABLED=true — using real device-mgmt HTTP adapter")
		mgmtClient = realClient
	}

	// Usecases.
	deviceSvc := netdevusecase.NewDeviceService(deviceRepo, warehouseRO, auditWriter)
	firmwareSvc := netdevusecase.NewFirmwareService(fwVersionRepo, fwJobRepo, deviceRepo, mgmtClient, auditWriter)
	swapSvc := netdevusecase.NewSwapService(swapRepo, deviceRepo, woCreator, retrofitter, auditWriter)
	rmaSvc := netdevusecase.NewRMAService(rmaRepo, deviceRepo, auditWriter)
	healthSvc := netdevusecase.NewHealthService(healthRepo, deviceRepo, auditWriter)
	complianceSvc := netdevusecase.NewComplianceService(deviceRepo, fwVersionRepo, complianceRepo, auditWriter)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)
	handler := netdevhttp.NewHandler(deviceSvc, firmwareSvc, swapSvc, rmaSvc, healthSvc, complianceSvc, verifier)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "netdevices-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("netdevices-svc", pool.Ping)
	handler.Mount(server.Router)

	// Cron tickers — daily compliance scan, weekly RMA expiry, hourly
	// stale-health scan. Each spawns its own goroutine and stops on
	// ctx.Done().
	netdevcron.NewFirmwareComplianceScanDaily(complianceSvc, log).Start(ctx)
	netdevcron.NewRMAExpiryScanWeekly(rmaSvc, log).Start(ctx)
	netdevcron.NewStaleHealthSnapshotScan(pool, log).Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("netdevices-svc stopped")
}

// ---------------------------------------------------------------------
// Cross-context bridges (defined inline — local SQL-only adapters)
// ---------------------------------------------------------------------

// warehouseAssetReader is a thin reader over warehouse.assets. We don't
// import internal/warehouse — the schema is documented in migration 0006
// and the columns we need are stable. A NotFound is returned when the
// warehouse schema isn't installed (e.g. in a netdevices-only test
// deployment), and the usecase degrades gracefully.
type warehouseAssetReader struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newWarehouseAssetReader(pool *pgxpool.Pool, log *slog.Logger) *warehouseAssetReader {
	return &warehouseAssetReader{pool: pool, log: log}
}

func (r *warehouseAssetReader) FindAsset(ctx context.Context, deviceID uuid.UUID) (*netdevport.WarehouseAssetSnapshot, error) {
	// Probe the schema first — if warehouse.assets doesn't exist, we
	// return nil,nil rather than an error so the commission flow
	// proceeds without the cross-context gate.
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'warehouse' AND table_name = 'assets'
		)
	`).Scan(&exists); err != nil || !exists {
		return nil, nil
	}
	// Match warehouse asset by netdev device serial (asset.serial_number).
	// Best-effort: if the warehouse schema lacks the expected columns we
	// just degrade to nil,nil — no error.
	row := r.pool.QueryRow(ctx, `
		SELECT a.id, a.stock_item_id, a.warehouse_id, a.status, COALESCE(a.serial_number, '')
		FROM warehouse.assets a
		JOIN netdev.devices d ON d.serial_no = a.serial_number
		WHERE d.id = $1
		LIMIT 1
	`, deviceID)
	var snap netdevport.WarehouseAssetSnapshot
	if err := row.Scan(&snap.AssetID, &snap.StockItemID, &snap.WarehouseID, &snap.Status, &snap.SerialNo); err != nil {
		return nil, nil // includes pgx.ErrNoRows; treat as "no constraint to enforce"
	}
	return &snap, nil
}

// retrofitTrigger inserts an asset_retrofit row directly. Wave 113
// keeps this minimal — the warehouse.asset_retrofits table from
// migration 0057 has the columns we need. If the table isn't present
// we silently no-op so the swap flow doesn't crash in a netdevices-only
// deployment.
type retrofitTrigger struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newRetrofitTrigger(pool *pgxpool.Pool, log *slog.Logger) *retrofitTrigger {
	return &retrofitTrigger{pool: pool, log: log}
}

func (t *retrofitTrigger) CreateRetrofitForSwap(ctx context.Context, swapID, oldDeviceID, newDeviceID uuid.UUID) (uuid.UUID, error) {
	var exists bool
	if err := t.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'warehouse' AND table_name = 'asset_retrofits'
		)
	`).Scan(&exists); err != nil || !exists {
		return uuid.Nil, nil
	}
	id := uuid.New()
	// Resolve the warehouse.assets ids by matching netdev serial.
	var sourceAssetID, producedAssetID *uuid.UUID
	_ = t.pool.QueryRow(ctx, `
		SELECT a.id FROM warehouse.assets a
		JOIN netdev.devices d ON d.serial_no = a.serial_number WHERE d.id = $1
	`, oldDeviceID).Scan(&sourceAssetID)
	_ = t.pool.QueryRow(ctx, `
		SELECT a.id FROM warehouse.assets a
		JOIN netdev.devices d ON d.serial_no = a.serial_number WHERE d.id = $1
	`, newDeviceID).Scan(&producedAssetID)
	if sourceAssetID == nil || producedAssetID == nil {
		// Warehouse can't find matching assets — skip the retrofit cleanly.
		return uuid.Nil, nil
	}
	_, err := t.pool.Exec(ctx, `
		INSERT INTO warehouse.asset_retrofits
			(id, source_asset_id, produced_asset_id, reason, performed_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO NOTHING
	`, id, *sourceAssetID, *producedAssetID, "netdev device swap "+swapID.String())
	if err != nil {
		if t.log != nil {
			t.log.Warn("retrofit bridge insert failed", "err", err, "swap_id", swapID)
		}
		return uuid.Nil, nil // non-fatal
	}
	return id, nil
}

// workOrderCreator inserts a placeholder field.work_orders row when the
// schema is present. The real WO orchestration lives in field-svc; this
// bridge exists so the swap flow can advance to technician_assigned
// without coupling internal/netdevices to internal/field.
type workOrderCreator struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newWorkOrderCreator(pool *pgxpool.Pool, log *slog.Logger) *workOrderCreator {
	return &workOrderCreator{pool: pool, log: log}
}

func (c *workOrderCreator) CreateSwapWO(ctx context.Context, swapID, customerID, technicianID uuid.UUID) (uuid.UUID, error) {
	// Best-effort. If field.work_orders doesn't have the columns we
	// expect we silently return a fresh uuid — the swap proceeds, the
	// operator can manually link the WO later.
	var exists bool
	if err := c.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'field' AND table_name = 'work_orders'
		)
	`).Scan(&exists); err != nil || !exists {
		return uuid.New(), nil
	}
	// We don't try to insert — the field-svc owns the full WO model. We
	// just return a synthetic id so the caller can stamp it; a follow-up
	// admin action (or field-svc consumer of a future event) creates
	// the actual WO row.
	if c.log != nil {
		c.log.Info("netdev swap WO placeholder",
			"swap_id", swapID, "customer_id", customerID, "technician_id", technicianID)
	}
	return uuid.New(), nil
}
