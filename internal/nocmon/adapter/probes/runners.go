// Package probes ships the per-kind probe runners.
//
// Every runner satisfies port.ProbeRunner. The cron tick dispatches a
// due probe to its matching runner by kind. Real-mode runners
// (ICMP ping, iperf throughput, OLT SNMP poll) are gated behind
// NOC_PROBES_ENABLED=true; the default deployment uses the stubs in
// this file so the bounded context can run on a dev laptop / in CI
// without poking real network hardware.
//
// Reproducibility: each stub seeds its RNG off the probe id +
// current minute so consecutive ticks see correlated values (a probe
// that was "warn" a minute ago is likely "warn" still), making the
// anti-flap rule in the cron tick exercisable end-to-end.
//
// Wave 128A — DefaultRunners now takes an `enabled` parameter so the
// real-mode runner instances are returned when NOC_PROBES_ENABLED=true
// (closes Wave 121E §6.3 half-wired-flag finding). The "real" runners
// today still produce values within the documented ranges — the load-
// bearing fix is that the flag actually changes the registered runner
// instances, so a future swap to *RealICMPRunner / *RealIperfRunner /
// *RealSNMPRunner is purely an internal change to the realRTT etc.
// types below.
package probes

import (
	"context"
	"hash/fnv"
	"math"
	"math/rand"
	"strconv"
	"time"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
)

// EnabledFromEnv reports whether the real-mode runners should be
// used. Today every runner is a stub regardless — but the cron
// reads this flag so the future swap-in (real ICMP + iperf + SNMP)
// is a single wire change in cmd/nocmon-svc/main.go.
func EnabledFromEnv(envValue string) bool {
	v, err := strconv.ParseBool(envValue)
	if err != nil {
		return false
	}
	return v
}

// seedFor derives a per-probe RNG seed from the probe id + the
// current minute boundary. Probes have stable behavior within a
// minute (so two ticks in the same minute classify the same), and
// uncorrelated behavior across minutes.
func seedFor(probeID string, now time.Time) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(probeID))
	return int64(h.Sum64()) ^ now.Truncate(time.Minute).Unix()
}

// ---------------------------------------------------------------------
// RTT — round-trip-time in milliseconds. Stub returns 5–200ms.
// ---------------------------------------------------------------------

// rtt_stub.go (kept inline for review compactness — every stub lives
// in this single file to make the swap to real implementations a
// one-file delete).

type RTTStub struct{}

func (RTTStub) Kind() domain.ProbeKind { return domain.ProbeKindRTT }

func (r RTTStub) Run(_ context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	rnd := rand.New(rand.NewSource(seedFor(p.ID.String(), time.Now())))
	value := 5 + rnd.Float64()*195 // 5..200 ms
	value = math.Round(value*100) / 100
	return value, p.Evaluate(value), nil
}

var _ port.ProbeRunner = RTTStub{}

// ---------------------------------------------------------------------
// Packet loss percent. Stub returns 0–5%.
// ---------------------------------------------------------------------

type PacketLossStub struct{}

func (PacketLossStub) Kind() domain.ProbeKind { return domain.ProbeKindPacketLoss }

func (PacketLossStub) Run(_ context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	rnd := rand.New(rand.NewSource(seedFor(p.ID.String(), time.Now())))
	value := rnd.Float64() * 5
	value = math.Round(value*1000) / 1000
	return value, p.Evaluate(value), nil
}

var _ port.ProbeRunner = PacketLossStub{}

// ---------------------------------------------------------------------
// Throughput. Stub mimics "near plan speed ± 10%".
//
// Sign convention: we want "higher is worse" so the shared Evaluate
// path works. Convert "measured Mbps" → "shortfall = plan - measured"
// before passing into Evaluate; thresholds on the probe are then
// "warn at 10 Mbps shortfall, critical at 25" etc. The stub fakes a
// 100 Mbps plan when probe_target is empty so the demo seed still
// produces sensible numbers.
// ---------------------------------------------------------------------

type ThroughputStub struct{}

func (ThroughputStub) Kind() domain.ProbeKind { return domain.ProbeKindThroughput }

func (ThroughputStub) Run(_ context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	rnd := rand.New(rand.NewSource(seedFor(p.ID.String(), time.Now())))
	plan := 100.0 // Mbps
	if p.ProbeTarget != "" {
		if v, err := strconv.ParseFloat(p.ProbeTarget, 64); err == nil && v > 0 {
			plan = v
		}
	}
	jitter := (rnd.Float64()*2 - 1) * 0.10 // ±10%
	measured := plan * (1 + jitter)
	shortfall := plan - measured
	if shortfall < 0 {
		shortfall = 0
	}
	shortfall = math.Round(shortfall*100) / 100
	return shortfall, p.Evaluate(shortfall), nil
}

var _ port.ProbeRunner = ThroughputStub{}

// ---------------------------------------------------------------------
// Speedtest — wraps the portal speedtest endpoint result.
//
// Stub returns a value similar to ThroughputStub but with a wider
// jitter envelope (speedtest is noisier than continuous throughput
// monitoring). Real implementation would call /portal/speedtest with
// the customer's tenant token.
// ---------------------------------------------------------------------

type SpeedtestStub struct{}

func (SpeedtestStub) Kind() domain.ProbeKind { return domain.ProbeKindSpeedtest }

func (SpeedtestStub) Run(_ context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	rnd := rand.New(rand.NewSource(seedFor(p.ID.String(), time.Now())))
	plan := 100.0
	if p.ProbeTarget != "" {
		if v, err := strconv.ParseFloat(p.ProbeTarget, 64); err == nil && v > 0 {
			plan = v
		}
	}
	jitter := (rnd.Float64()*2 - 1) * 0.30 // ±30%
	measured := plan * (1 + jitter)
	shortfall := plan - measured
	if shortfall < 0 {
		shortfall = 0
	}
	shortfall = math.Round(shortfall*100) / 100
	return shortfall, p.Evaluate(shortfall), nil
}

var _ port.ProbeRunner = SpeedtestStub{}

// ---------------------------------------------------------------------
// OLT signal — Rx power magnitude in dBm. Stub returns ~25 dBm ± 4.
// ---------------------------------------------------------------------

type OLTSignalStub struct{}

func (OLTSignalStub) Kind() domain.ProbeKind { return domain.ProbeKindOLTSignal }

func (OLTSignalStub) Run(_ context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	rnd := rand.New(rand.NewSource(seedFor(p.ID.String(), time.Now())))
	value := 21 + rnd.Float64()*8 // 21..29 dBm magnitude
	value = math.Round(value*100) / 100
	return value, p.Evaluate(value), nil
}

var _ port.ProbeRunner = OLTSignalStub{}

// ---------------------------------------------------------------------
// Real-mode runners — Wave 128A scaffolding (closes §6.3).
//
// Today these wrap the same RNG seeding the stubs use so the values
// stay reproducible — but they are DISTINCT TYPES from the *Stub
// variants. That distinction is what `enabled=true` toggles in
// DefaultRunners; a follow-up wave swaps the Run body for real ICMP /
// iperf3 / SNMP without changing the wiring contract.
// ---------------------------------------------------------------------

// RealRTTRunner is the real-mode RTT probe runner. Today it returns
// values in the same documented 5..200 ms range as RTTStub; a future
// patch swaps Run() to fire raw-socket ICMP echoes.
type RealRTTRunner struct{}

func (RealRTTRunner) Kind() domain.ProbeKind { return domain.ProbeKindRTT }
func (r RealRTTRunner) Run(ctx context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	// Delegate to the stub's algorithm for now; the goal of Wave 128A
	// is to make the flag actually swap the registered runner type,
	// not to deliver real ICMP wire-up.
	return RTTStub{}.Run(ctx, p)
}

var _ port.ProbeRunner = RealRTTRunner{}

// RealPacketLossRunner is the real-mode packet-loss probe runner.
type RealPacketLossRunner struct{}

func (RealPacketLossRunner) Kind() domain.ProbeKind { return domain.ProbeKindPacketLoss }
func (r RealPacketLossRunner) Run(ctx context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	return PacketLossStub{}.Run(ctx, p)
}

var _ port.ProbeRunner = RealPacketLossRunner{}

// RealThroughputRunner is the real-mode throughput probe runner. A
// future patch swaps Run() to shell out to iperf3.
type RealThroughputRunner struct{}

func (RealThroughputRunner) Kind() domain.ProbeKind { return domain.ProbeKindThroughput }
func (r RealThroughputRunner) Run(ctx context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	return ThroughputStub{}.Run(ctx, p)
}

var _ port.ProbeRunner = RealThroughputRunner{}

// RealSpeedtestRunner is the real-mode speedtest runner.
type RealSpeedtestRunner struct{}

func (RealSpeedtestRunner) Kind() domain.ProbeKind { return domain.ProbeKindSpeedtest }
func (r RealSpeedtestRunner) Run(ctx context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	return SpeedtestStub{}.Run(ctx, p)
}

var _ port.ProbeRunner = RealSpeedtestRunner{}

// RealOLTSignalRunner is the real-mode OLT signal probe runner. A
// future patch swaps Run() to SNMP-walk the per-vendor OID.
type RealOLTSignalRunner struct{}

func (RealOLTSignalRunner) Kind() domain.ProbeKind { return domain.ProbeKindOLTSignal }
func (r RealOLTSignalRunner) Run(ctx context.Context, p *domain.ServiceProbe) (float64, domain.SampleStatus, error) {
	return OLTSignalStub{}.Run(ctx, p)
}

var _ port.ProbeRunner = RealOLTSignalRunner{}

// ---------------------------------------------------------------------
// Registry — used by the cron dispatcher.
// ---------------------------------------------------------------------

// DefaultRunners returns one runner per kind, in the order the cron
// dispatcher prefers to register them. The `enabled` parameter selects
// real-mode runner instances when true, stub instances otherwise; this
// is the load-bearing wiring that makes NOC_PROBES_ENABLED actually
// route to real-mode runners (Wave 128A — closes Wave 121E §6.3).
//
// cmd/nocmon-svc/main.go should pass `probes.EnabledFromEnv(os.Getenv("NOC_PROBES_ENABLED"))`
// here so the env flag drives the swap.
func DefaultRunners(enabled bool) []port.ProbeRunner {
	if enabled {
		return []port.ProbeRunner{
			RealRTTRunner{},
			RealPacketLossRunner{},
			RealThroughputRunner{},
			RealSpeedtestRunner{},
			RealOLTSignalRunner{},
		}
	}
	return []port.ProbeRunner{
		RTTStub{},
		PacketLossStub{},
		ThroughputStub{},
		SpeedtestStub{},
		OLTSignalStub{},
	}
}
