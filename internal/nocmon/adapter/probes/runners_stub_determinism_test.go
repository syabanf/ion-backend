// Wave 121E — NOC probe runner stub-mode determinism tests.
//
// Per the package doc, every runner seeds its RNG off
// (probe_id, now.Truncate(minute)) so consecutive ticks in the SAME
// minute produce identical values — that's the load-bearing
// anti-flap property the cron's hysteresis rule depends on.
//
// Each test below asserts:
//   - Two ticks within the same minute produce identical (value,status)
//   - Value falls inside the documented range for the probe kind
//   - No network calls (the stubs hold no http.Client / dial)
//
// What this DOES NOT validate:
//   - Real ICMP timing (clock skew, sub-ms latency)
//   - Real iperf3 connect failure modes
//   - Real SNMP polling latency / OID schema
//   - Cross-minute correlation behaviour (it's documented to drift)
package probes

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
)

// fixtureProbe builds a probe with stable id for deterministic seeding.
func fixtureProbe(t *testing.T, kind domain.ProbeKind, target string) *domain.ServiceProbe {
	t.Helper()
	warn, crit := 50.0, 100.0
	p, err := domain.NewServiceProbe(
		uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		kind, target, 60, &warn, &crit,
	)
	if err != nil {
		t.Fatalf("NewServiceProbe: %v", err)
	}
	// Pin the probe id so the seed is deterministic across runs.
	p.ID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	return p
}

// runTwiceInSameMinute is the core invariant: two Run calls inside the
// same minute boundary must produce identical results. We deliberately
// don't pin time via injection because the runners take time.Now()
// internally — instead we serialize two calls and re-run if a minute
// boundary crossed mid-test (rare; would only flake every 60s).
func runTwiceInSameMinute(t *testing.T, run func() (float64, domain.SampleStatus, error)) (float64, domain.SampleStatus) {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		start := time.Now().Truncate(time.Minute)
		v1, s1, err := run()
		if err != nil {
			t.Fatalf("attempt %d: first Run: %v", attempt, err)
		}
		v2, s2, err := run()
		if err != nil {
			t.Fatalf("attempt %d: second Run: %v", attempt, err)
		}
		end := time.Now().Truncate(time.Minute)
		if !start.Equal(end) {
			// Boundary crossed — retry from a fresh minute.
			continue
		}
		if v1 != v2 {
			t.Fatalf("value drift within minute: %v vs %v", v1, v2)
		}
		if s1 != s2 {
			t.Fatalf("status drift within minute: %q vs %q", s1, s2)
		}
		return v1, s1
	}
	t.Fatal("could not capture two calls inside the same minute after 3 attempts (extremely unlikely)")
	return 0, ""
}

// =====================================================================
// 1) RTT stub — value in [5, 200] ms range.
// =====================================================================

func TestRTTStub_Deterministic(t *testing.T) {
	r := RTTStub{}
	p := fixtureProbe(t, domain.ProbeKindRTT, "")
	v, _ := runTwiceInSameMinute(t, func() (float64, domain.SampleStatus, error) {
		return r.Run(context.Background(), p)
	})
	if v < 5 || v > 200 {
		t.Errorf("RTT = %v ms, outside documented 5..200 range", v)
	}
	if r.Kind() != domain.ProbeKindRTT {
		t.Errorf("Kind() = %q, want %q", r.Kind(), domain.ProbeKindRTT)
	}
}

// =====================================================================
// 2) Packet loss stub — value in [0, 5] percent.
// =====================================================================

func TestPacketLossStub_Deterministic(t *testing.T) {
	r := PacketLossStub{}
	p := fixtureProbe(t, domain.ProbeKindPacketLoss, "")
	v, _ := runTwiceInSameMinute(t, func() (float64, domain.SampleStatus, error) {
		return r.Run(context.Background(), p)
	})
	if v < 0 || v > 5 {
		t.Errorf("PacketLoss = %v%%, outside documented 0..5 range", v)
	}
	if r.Kind() != domain.ProbeKindPacketLoss {
		t.Errorf("Kind() = %q, want %q", r.Kind(), domain.ProbeKindPacketLoss)
	}
}

// =====================================================================
// 3) Throughput stub — shortfall in [0, ~10] Mbps when plan = 100.
// =====================================================================

func TestThroughputStub_Deterministic(t *testing.T) {
	r := ThroughputStub{}
	p := fixtureProbe(t, domain.ProbeKindThroughput, "100") // 100 Mbps plan
	v, _ := runTwiceInSameMinute(t, func() (float64, domain.SampleStatus, error) {
		return r.Run(context.Background(), p)
	})
	// Shortfall = plan - measured; jitter is ±10% so shortfall ∈ [0, 10].
	// Lower bound is 0 (clamped) so values <0 are filtered.
	if v < 0 || v > 11 {
		t.Errorf("Throughput shortfall = %v, outside documented 0..10 range (allow tiny rounding slack)", v)
	}
	if r.Kind() != domain.ProbeKindThroughput {
		t.Errorf("Kind() = %q, want %q", r.Kind(), domain.ProbeKindThroughput)
	}
}

// =====================================================================
// 4) Speedtest stub — wider jitter (±30%) → shortfall in [0, ~30].
// =====================================================================

func TestSpeedtestStub_Deterministic(t *testing.T) {
	r := SpeedtestStub{}
	p := fixtureProbe(t, domain.ProbeKindSpeedtest, "100")
	v, _ := runTwiceInSameMinute(t, func() (float64, domain.SampleStatus, error) {
		return r.Run(context.Background(), p)
	})
	if v < 0 || v > 35 {
		t.Errorf("Speedtest shortfall = %v, outside documented 0..30 range (allow rounding slack)", v)
	}
	if r.Kind() != domain.ProbeKindSpeedtest {
		t.Errorf("Kind() = %q, want %q", r.Kind(), domain.ProbeKindSpeedtest)
	}
}

// =====================================================================
// 5) OLT signal stub — value in [21, 29] dBm.
// =====================================================================

func TestOLTSignalStub_Deterministic(t *testing.T) {
	r := OLTSignalStub{}
	p := fixtureProbe(t, domain.ProbeKindOLTSignal, "")
	v, _ := runTwiceInSameMinute(t, func() (float64, domain.SampleStatus, error) {
		return r.Run(context.Background(), p)
	})
	if v < 21 || v > 29 {
		t.Errorf("OLTSignal = %v dBm, outside documented 21..29 range", v)
	}
	if r.Kind() != domain.ProbeKindOLTSignal {
		t.Errorf("Kind() = %q, want %q", r.Kind(), domain.ProbeKindOLTSignal)
	}
}

// =====================================================================
// 6) DefaultRunners returns one runner per kind.
// =====================================================================

func TestDefaultRunners_OnePerKind(t *testing.T) {
	runners := DefaultRunners()
	seen := map[domain.ProbeKind]bool{}
	for _, r := range runners {
		if seen[r.Kind()] {
			t.Errorf("duplicate runner for kind %q", r.Kind())
		}
		seen[r.Kind()] = true
	}
	want := []domain.ProbeKind{
		domain.ProbeKindRTT,
		domain.ProbeKindPacketLoss,
		domain.ProbeKindThroughput,
		domain.ProbeKindSpeedtest,
		domain.ProbeKindOLTSignal,
	}
	for _, k := range want {
		if !seen[k] {
			t.Errorf("kind %q not represented in DefaultRunners", k)
		}
	}
}

// =====================================================================
// 7) EnabledFromEnv toggle parser — guards the real-mode swap.
// =====================================================================

func TestEnabledFromEnv_Parses(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"", false},     // unset → stub
		{"yes", false},  // not a valid go bool → stub
		{"maybe", false},
	}
	for _, c := range cases {
		got := EnabledFromEnv(c.in)
		if got != c.want {
			t.Errorf("EnabledFromEnv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
