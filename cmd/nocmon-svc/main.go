// nocmon-svc — NOC monitoring bounded context service.
//
// Wave 112 scope: service probes (RTT / packet loss / throughput /
// speedtest / OLT signal), fiber attenuation monitoring, fault
// events + state machine, fault impact analysis, topology snapshot
// reads, and the "create maintenance WO from alert" bridge.
//
// Isolated bounded context: this service speaks only to its own
// `nocmon.*` schema. Cross-context UUIDs (customer_id, olt_port_id,
// plan_id, etc.) are stored as plain UUIDs and resolved by the
// calling service at display time. The two cross-context ports
// (TopologyBuilder, WorkOrderCreator) are wired here as thin
// in-process adapters; the future extraction into a standalone
// microservice swaps those for real RPC/HTTP clients without
// touching domain rules.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/adapter/http"
	"github.com/ion-core/backend/internal/nocmon/adapter/postgres"
	"github.com/ion-core/backend/internal/nocmon/adapter/probes"
	"github.com/ion-core/backend/internal/nocmon/cron"
	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	nocusecase "github.com/ion-core/backend/internal/nocmon/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
)

func main() {
	cfg, err := config.Load("NOCMON_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "nocmon-svc")
	log.Info("starting nocmon-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories — one per aggregate. All bound to the `nocmon`
	// schema; no cross-schema queries.
	probeRepo := postgres.NewServiceProbeRepository(pool)
	sampleRepo := postgres.NewHealthSampleRepository(pool)
	fiberRepo := postgres.NewFiberLinkRepository(pool)
	faultRepo := postgres.NewFaultEventRepository(pool)
	impactRepo := postgres.NewFaultImpactRepository(pool)
	topologyRepo := postgres.NewTopologySnapshotRepository(pool)

	// Cross-context adapters — defined inline to keep the wiring
	// explicit and the bounded context dependency-clean.
	topologyBuilder := stubTopologyBuilder{}
	woCreator := nocusecase.StubWorkOrderCreator{}

	// Usecase services. The audit writer is nil here (passes through
	// to audit.Nop{} inside the services) — a future wave will swap
	// in pkg/audit/postgres so the dashboard timeline picks up state
	// transitions.
	probeSvc := nocusecase.NewProbeService(probeRepo, sampleRepo, nil)
	fiberSvc := nocusecase.NewFiberService(fiberRepo, nil)
	faultSvc := nocusecase.NewFaultService(faultRepo, impactRepo, nil)
	topologySvc := nocusecase.NewTopologyService(topologyRepo, topologyBuilder)
	alertWOSvc := nocusecase.NewAlertWOService(faultRepo, impactRepo, woCreator, nil)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	handler := http.NewHandler(probeSvc, fiberSvc, faultSvc, topologySvc, alertWOSvc, verifier)

	// Cron — probes tick every 60s, fiber attenuation daily,
	// fault digest hourly. Runners default to the stub set; flip
	// NOC_PROBES_ENABLED=true to enable real-mode (today still
	// stubs — the real ICMP/iperf/SNMP wiring lands in a follow-up).
	enabled := probes.EnabledFromEnv(os.Getenv("NOC_PROBES_ENABLED"))
	cronRunner := cron.
		New(pool, log, probeRepo, sampleRepo, fiberRepo, faultRepo, faultSvc, nil).
		WithProbeRunners(probes.DefaultRunners(enabled)).
		WithEnabled(enabled)
	cronRunner.Start(ctx)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "nocmon-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("nocmon-svc", pool.Ping)
	handler.Mount(server.Router)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("nocmon-svc stopped")
}

// ---------------------------------------------------------------------
// Cross-context adapter stubs
// ---------------------------------------------------------------------

// stubTopologyBuilder returns an empty-but-valid TopologySnapshot
// payload. The real builder will read from internal/network/* via a
// thin in-process reader — kept stubbed in Wave 112 so the bounded
// context compiles + runs without a hard dep on the network repo
// (which lives in a sibling context). The TopologyBuilder interface
// is the single seam to swap in the real implementation later.
type stubTopologyBuilder struct{}

func (stubTopologyBuilder) BuildSnapshot(_ context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error) {
	payload, _ := json.Marshal(map[string]any{
		"nodes":  []any{},
		"edges":  []any{},
		"scope":  string(scope),
		"_stub":  true,
	})
	return domain.NewTopologySnapshot(scope, scopeID, payload, 0, 0)
}

var _ port.TopologyBuilder = stubTopologyBuilder{}
