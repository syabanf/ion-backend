// Wave 121E (extended in Wave 128A) — NOC probe runners env-flag toggle tests.
//
// Wave 121E pinned that NOC_PROBES_ENABLED was a half-wired flag —
// EnabledFromEnv parsed correctly but DefaultRunners returned stubs
// regardless.
//
// Wave 128A closes that finding: DefaultRunners now takes an `enabled`
// parameter, and cmd/nocmon-svc passes EnabledFromEnv(env) into it.
// The real-mode runner *types* are RealRTTRunner / RealPacketLossRunner
// / RealThroughputRunner / RealSpeedtestRunner / RealOLTSignalRunner;
// today their Run() bodies delegate to the stubs (range-preserving)
// but the load-bearing fix is that the flag actually changes the
// registered runner instances.
package probes

import (
	"reflect"
	"testing"

	"github.com/ion-core/backend/internal/nocmon/domain"
)

// =====================================================================
// 1) EnabledFromEnv parses both Go-canonical bool literals.
// =====================================================================

func TestProbes_EnabledFromEnv_BogusValuesDefaultToDisabled(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"", false},       // unset → disabled
		{"truthy", false}, // junk → disabled
		{"1", true},
		{"0", false},
		{"TRUE", true},
		{"Yes", false}, // not a strconv.ParseBool literal
	}
	for _, c := range cases {
		if got := EnabledFromEnv(c.in); got != c.want {
			t.Errorf("EnabledFromEnv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// =====================================================================
// 2) DefaultRunners — coverage by kind is stable across the flag.
//
// Both enabled=true and enabled=false must register exactly one runner
// per ProbeKind so the cron dispatcher doesn't end up with a hole.
// =====================================================================

func TestProbes_DefaultRunners_KindCoverageStableAcrossFlag(t *testing.T) {
	enabled := DefaultRunners(true)
	disabled := DefaultRunners(false)

	if len(enabled) != len(disabled) {
		t.Errorf("runner count drifts with flag: enabled=%d disabled=%d", len(enabled), len(disabled))
	}
	for i := range enabled {
		if enabled[i].Kind() != disabled[i].Kind() {
			t.Errorf("runner %d kind drifts: enabled=%q disabled=%q",
				i, enabled[i].Kind(), disabled[i].Kind())
		}
	}
}

// =====================================================================
// 3) Wave 128A — enabled=true returns DIFFERENT runner instances than
// enabled=false.
//
// This is the load-bearing closure of Wave 121E §6.3: flipping the flag
// must actually swap the registered runner types, not just log and
// return the same stubs.
// =====================================================================

func TestProbes_DefaultRunners_EnabledSwapsRunnerTypes(t *testing.T) {
	enabled := DefaultRunners(true)
	disabled := DefaultRunners(false)

	if len(enabled) != len(disabled) {
		t.Fatalf("runner count drifts: enabled=%d disabled=%d", len(enabled), len(disabled))
	}
	for i := range enabled {
		enabledType := reflect.TypeOf(enabled[i])
		disabledType := reflect.TypeOf(disabled[i])
		if enabledType == disabledType {
			t.Errorf("runner %d (kind %q): enabled and disabled returned same type %v — flag is a no-op",
				i, enabled[i].Kind(), enabledType)
		}
	}
}

// =====================================================================
// 4) Wave 128A — disabled returns the documented stub types so the
// dev/CI default (no real network hits) is preserved.
// =====================================================================

func TestProbes_DefaultRunners_DisabledReturnsStubs(t *testing.T) {
	disabled := DefaultRunners(false)
	wantTypes := map[domain.ProbeKind]string{
		domain.ProbeKindRTT:        "probes.RTTStub",
		domain.ProbeKindPacketLoss: "probes.PacketLossStub",
		domain.ProbeKindThroughput: "probes.ThroughputStub",
		domain.ProbeKindSpeedtest:  "probes.SpeedtestStub",
		domain.ProbeKindOLTSignal:  "probes.OLTSignalStub",
	}
	for _, r := range disabled {
		got := reflect.TypeOf(r).String()
		if want := wantTypes[r.Kind()]; got != want {
			t.Errorf("kind %q: got %s, want %s", r.Kind(), got, want)
		}
	}
}

// =====================================================================
// 5) Wave 128A — enabled returns the documented real-mode types.
// =====================================================================

func TestProbes_DefaultRunners_EnabledReturnsRealRunners(t *testing.T) {
	enabled := DefaultRunners(true)
	wantTypes := map[domain.ProbeKind]string{
		domain.ProbeKindRTT:        "probes.RealRTTRunner",
		domain.ProbeKindPacketLoss: "probes.RealPacketLossRunner",
		domain.ProbeKindThroughput: "probes.RealThroughputRunner",
		domain.ProbeKindSpeedtest:  "probes.RealSpeedtestRunner",
		domain.ProbeKindOLTSignal:  "probes.RealOLTSignalRunner",
	}
	for _, r := range enabled {
		got := reflect.TypeOf(r).String()
		if want := wantTypes[r.Kind()]; got != want {
			t.Errorf("kind %q: got %s, want %s", r.Kind(), got, want)
		}
	}
}
