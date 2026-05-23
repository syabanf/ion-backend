// warehouse-svc — Warehouse & Asset service.
//
// Round-1 scope: warehouses, stock catalog, intake, inventory dashboard,
// asset registry, movement audit, inter-warehouse transfers. Round 2 adds
// threshold escalation, opname workflow, and asset retrofit.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	warehouseconfig "github.com/ion-core/backend/internal/warehouse/adapter/config"
	warehousehttp "github.com/ion-core/backend/internal/warehouse/adapter/http"
	warehousepg "github.com/ion-core/backend/internal/warehouse/adapter/postgres"
	warehouseqr "github.com/ion-core/backend/internal/warehouse/adapter/qr"
	warehouseusecase "github.com/ion-core/backend/internal/warehouse/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/platformconfig"
)

func main() {
	cfg, err := config.Load("WAREHOUSE_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "warehouse-svc")
	log.Info("starting warehouse-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	whRepo := warehousepg.NewWarehouseRepository(pool)
	itemRepo := warehousepg.NewStockItemRepository(pool)
	assetRepo := warehousepg.NewAssetRepository(pool)
	levelRepo := warehousepg.NewStockLevelRepository(pool)
	moveRepo := warehousepg.NewMovementRepository(pool)
	invRepo := warehousepg.NewInventoryRepository(pool)
	xferRepo := warehousepg.NewTransferRepository(pool)
	// M3 r2 — thresholds, alerts, opname workflow.
	thresholdRepo := warehousepg.NewThresholdRepository(pool)
	alertRepo := warehousepg.NewAlertRepository(pool)
	opnameRepo := warehousepg.NewOpnameRepository(pool)
	// Supplier registry (CRM-Sales-Enterprise PRD §5.1).
	supplierRepo := warehousepg.NewSupplierRepository(pool)
	// WO dispatch — BOM + QR-scan handoff from warehouse to field tech.
	woDispatchRepo := warehousepg.NewWODispatchRepository(pool)
	// Wave 85 (Tier 3 starter) — purchase orders.
	poRepo := warehousepg.NewPurchaseOrderRepository(pool)
	// Wave 86 — goods receipts (one-shot atomic tx; depends on poRepo).
	grRepo := warehousepg.NewGoodsReceiptRepository(pool)
	// Wave 87 — asset retrofit.
	retrofitRepo := warehousepg.NewAssetRetrofitRepository(pool)
	// Wave 89 — product BOM templates.
	bomRepo := warehousepg.NewProductBOMRepository(pool)

	// Wave 117 — warehouse depth: item categories, cable, consumable,
	// sub-warehouse, asset location, opname tablet, QR + netdev bridge.
	itemCatRepo := warehousepg.NewItemCategoryRepository(pool)
	cableLotRepo := warehousepg.NewCableLotRepository(pool)
	cableCutRepo := warehousepg.NewCableCutRepository(pool)
	consBatchRepo := warehousepg.NewConsumableBatchRepository(pool)
	consLogRepo := warehousepg.NewBatchConsumptionLogRepository(pool)
	subWHRepo := warehousepg.NewSubWarehouseRepository(pool)
	assetLocRepo := warehousepg.NewAssetLocationHistoryRepository(pool)
	opnTabletRepo := warehousepg.NewOpnameTabletSessionRepository(pool)
	netdevBridge := warehousepg.NewNetdevBridge(pool)
	qrGen := warehouseqr.New()

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	// platform_config reader → FIFO/LIFO default for inventory + asset
	// listings. The reader caches values for 60s (the PRD's "config
	// changes propagate within 60s" budget) so this is cheap on the
	// hot path.
	pcReader := platformconfig.New(pool)
	valuation := warehouseconfig.NewValuationReader(pcReader)

	svc := warehouseusecase.NewService(whRepo, itemRepo, assetRepo, levelRepo, moveRepo, invRepo, xferRepo, log).
		WithR2(thresholdRepo, alertRepo, opnameRepo).
		WithSuppliers(supplierRepo).
		WithValuation(valuation).
		WithWODispatch(woDispatchRepo).
		WithPurchaseOrders(poRepo).
		WithGoodsReceipts(grRepo).
		WithAssetRetrofits(retrofitRepo).
		WithBOMTemplates(bomRepo).
		// Wave 117 wiring — opt-in surfaces; nil-safe if any single repo
		// fails to construct, the corresponding endpoints surface "not
		// configured" instead of panicking on startup.
		WithItemCategories(itemCatRepo).
		WithCable(cableLotRepo, cableCutRepo).
		WithConsumables(consBatchRepo, consLogRepo).
		WithSubWarehouses(subWHRepo).
		WithAssetLocations(assetLocRepo).
		WithOpnameTablet(opnTabletRepo).
		WithQRGenerator(qrGen).
		WithNetdevWriter(netdevBridge)

	handler := warehousehttp.NewHandler(svc, verifier).WithWODispatch(svc).WithDepth(svc)
	priorityHandler := warehousehttp.NewPriorityHandler(pool, verifier)
	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "warehouse-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("warehouse-svc", pool.Ping)
	// Wave 105 — Prometheus instrumentation + /metrics scrape endpoint.
	handler.Mount(server.Router)
	priorityHandler.Mount(server.Router)

	// Wave 88b — alert cascade tick. Runs hourly; each pass is
	// idempotent (SyncAlertStates is INSERT/UPDATE-conditional on
	// stock_levels state, CascadeEscalations only bumps rows past
	// their budget). Default escalation budgets follow PRD §10:
	//   sub_area → area   after 24h
	//   area     → regional after 24h
	//
	// Like the billing tick, this is in-process for now; Round-4
	// moves it out via leader election when we go multi-replica.
	// notifyx push to managers at each level lands in Wave 88c —
	// needs the branch→manager gateway that doesn't exist yet.
	const (
		alertTickInterval = time.Hour
		subToAreaBudget   = 24 * time.Hour
		areaToRegionalBudget = 24 * time.Hour
	)
	go func() {
		t := time.NewTicker(alertTickInterval)
		defer t.Stop()
		runTick := func() {
			opened, closed, escalated, err := svc.RunAlertCascadeTick(ctx, subToAreaBudget, areaToRegionalBudget)
			if err != nil {
				log.Error("alert cascade tick failed", "err", err)
				return
			}
			// Only log when something happened — silent ticks would
			// flood the logs in healthy clusters.
			if opened > 0 || closed > 0 || escalated > 0 {
				log.Info("alert cascade tick",
					"opened", opened, "closed", closed, "escalated", escalated)
			}
		}
		runTick() // immediate tick on startup
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runTick()
			}
		}
	}()

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("warehouse-svc stopped")
}
