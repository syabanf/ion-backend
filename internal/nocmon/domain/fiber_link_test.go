package domain

import (
	"testing"
	"time"
)

// TestEvaluateAttenuation covers TC-NFA-001/002 — Rx-power threshold
// warning and critical tiers using the GPON defaults.
func TestEvaluateAttenuation(t *testing.T) {
	link := &FiberLink{
		WarnThresholdDB:     25.0,
		CriticalThresholdDB: 28.0,
	}
	now := time.Now().UTC()
	cases := []struct {
		measured float64
		expected FiberStatus
		desc     string
	}{
		{22.0, FiberStatusOK, "well under warn"},
		{25.0, FiberStatusOK, "at warn boundary still ok (strict >)"},
		{26.0, FiberStatusWarn, "between warn and critical"},
		{28.0, FiberStatusWarn, "at critical boundary still warn (strict >)"},
		{28.5, FiberStatusCritical, "above critical"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := link.EvaluateAttenuation(tc.measured, now)
			if got != tc.expected {
				t.Errorf("measured=%v: got %s, want %s", tc.measured, got, tc.expected)
			}
		})
	}
}

func TestFiberLinkIsOffline(t *testing.T) {
	link := &FiberLink{}
	now := time.Now().UTC()
	// No measurement yet → not offline (would be "unknown" instead).
	if link.IsOffline(now) {
		t.Errorf("link with no measurement should not be offline")
	}
	recent := now.Add(-1 * time.Hour)
	link.LastMeasuredAt = &recent
	if link.IsOffline(now) {
		t.Errorf("recently-measured link should not be offline")
	}
	stale := now.Add(-48 * time.Hour)
	link.LastMeasuredAt = &stale
	if !link.IsOffline(now) {
		t.Errorf("48h-old link should be offline")
	}
}

func TestFiberLinkValidateThresholds(t *testing.T) {
	cases := []struct {
		warn, crit float64
		wantErr    bool
		desc       string
	}{
		{25.0, 28.0, false, "GPON defaults"},
		{-1.0, 28.0, true, "negative warn"},
		{25.0, -1.0, true, "negative critical"},
		{30.0, 25.0, true, "critical < warn"},
		{25.0, 25.0, false, "critical == warn — degenerate but allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &FiberLink{WarnThresholdDB: tc.warn, CriticalThresholdDB: tc.crit}
			err := l.ValidateThresholds()
			if (err != nil) != tc.wantErr {
				t.Errorf("got err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
