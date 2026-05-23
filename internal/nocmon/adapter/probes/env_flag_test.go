// Wave 121E — NOC probe runners env-flag toggle tests.
//
// The runners file documents the swap matrix as a "one-file delete"
// (replace each stub with the real ICMP / iperf3 / SNMP runner). Today
// NOC_PROBES_ENABLED has no effect on the package itself — the cron
// reads the flag and would route to real runners when they exist.
//
// What we CAN pin today:
//   - EnabledFromEnv parses correctly for documented inputs.
//   - DefaultRunners is consistent (count + kind coverage) regardless
//     of the env flag.
//
// When real runners land, this file's TestRealRunners_* test is the
// landing pad — assert that NOC_PROBES_ENABLED=true causes
// DefaultRunners (or a new RealRunners constructor) to return runners
// whose Run() actually hits the network.
package probes

import (
	"testing"

	"github.com/ion-core/backend/internal/nocmon/domain"
)

// =====================================================================
// 1) EnabledFromEnv parses both Go-canonical bool literals.
//
// Already covered in determinism test, but here we additionally pin
// that bogus values default to disabled rather than panicking.
// =====================================================================

func TestProbes_EnabledFromEnv_BogusValuesDefaultToDisabled(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"", false},          // unset → disabled
		{"truthy", false},    // junk → disabled
		{"1", true},
		{"0", false},
		{"TRUE", true},
		{"Yes", false},       // not a strconv.ParseBool literal
	}
	for _, c := range cases {
		if got := EnabledFromEnv(c.in); got != c.want {
			t.Errorf("EnabledFromEnv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// =====================================================================
// 2) DefaultRunners is stable regardless of env (the env is read by the
// cron, not by this constructor).
// =====================================================================

func TestProbes_DefaultRunners_StableAcrossEnvFlag(t *testing.T) {
	t.Setenv("NOC_PROBES_ENABLED", "true")
	enabled := DefaultRunners()

	t.Setenv("NOC_PROBES_ENABLED", "false")
	disabled := DefaultRunners()

	if len(enabled) != len(disabled) {
		t.Errorf("runner count drifts with env flag: enabled=%d disabled=%d", len(enabled), len(disabled))
	}
	for i := range enabled {
		if enabled[i].Kind() != disabled[i].Kind() {
			t.Errorf("runner %d kind drifts: enabled=%q disabled=%q", i, enabled[i].Kind(), disabled[i].Kind())
		}
	}
}

// =====================================================================
// 3) Real-mode runners — not yet implemented.
//
// When real ICMP / iperf3 / SNMP runners land, swap this Skip for a
// real assertion that DefaultRunners returns (e.g.) *RealICMPRunner
// when NOC_PROBES_ENABLED=true.
// =====================================================================

func TestProbes_RealMode_NotYetImplemented(t *testing.T) {
	t.Setenv("NOC_PROBES_ENABLED", "true")
	t.Skip("Wave 121E: real ICMP/iperf3/SNMP runners not yet implemented; tracked in production wiring readiness doc")

	// Future assertion shape:
	//   runners := DefaultRunners()
	//   for _, r := range runners {
	//       if _, ok := r.(*RealICMPRunner); ok { ... }
	//   }
	_ = domain.ProbeKindRTT
}
