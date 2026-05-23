package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptrFloat(f float64) *float64 { return &f }

func TestServiceProbeEvaluate(t *testing.T) {
	// RTT probe: warn at 50ms, critical at 100ms.
	p, err := NewServiceProbe(uuid.New(), ProbeKindRTT, "8.8.8.8", 60, ptrFloat(50), ptrFloat(100))
	if err != nil {
		t.Fatalf("construct probe: %v", err)
	}
	cases := []struct {
		value    float64
		expected SampleStatus
		desc     string
	}{
		{10, SampleStatusOK, "well under warn"},
		{49.9, SampleStatusOK, "just under warn"},
		{55, SampleStatusWarn, "between warn and critical"},
		{105, SampleStatusCritical, "above critical"},
		{100.001, SampleStatusCritical, "just over critical"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := p.Evaluate(tc.value)
			if got != tc.expected {
				t.Errorf("value=%v: got %s, want %s", tc.value, got, tc.expected)
			}
		})
	}
}

func TestServiceProbeEvaluateNilThresholdsAreOK(t *testing.T) {
	// Probe with NO thresholds at all — every reading should classify
	// as ok (informational only).
	p, err := NewServiceProbe(uuid.New(), ProbeKindRTT, "x", 60, nil, nil)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if got := p.Evaluate(9999); got != SampleStatusOK {
		t.Errorf("expected ok, got %s", got)
	}
}

func TestServiceProbeIsStale(t *testing.T) {
	p, _ := NewServiceProbe(uuid.New(), ProbeKindRTT, "", 60, nil, nil)
	now := time.Now().UTC()
	// Never run yet → stale.
	if !p.IsStale(now) {
		t.Errorf("freshly-constructed probe should be stale")
	}
	// Just ran → not stale.
	just := now.Add(-10 * time.Second)
	p.LastProbedAt = &just
	if p.IsStale(now) {
		t.Errorf("recently-probed probe should not be stale")
	}
	// Past interval → stale.
	long := now.Add(-2 * time.Minute)
	p.LastProbedAt = &long
	if !p.IsStale(now) {
		t.Errorf("old-probed probe should be stale")
	}
	// Inactive → never stale.
	p.IsActive = false
	if p.IsStale(now) {
		t.Errorf("inactive probe should never be stale")
	}
}

func TestServiceProbeKindValid(t *testing.T) {
	if ProbeKind("nope").Valid() {
		t.Errorf("invalid kind should not validate")
	}
	for _, k := range []ProbeKind{ProbeKindRTT, ProbeKindPacketLoss, ProbeKindThroughput, ProbeKindSpeedtest, ProbeKindOLTSignal} {
		if !k.Valid() {
			t.Errorf("kind %s should be valid", k)
		}
	}
}

func TestNewServiceProbeRejectsBadInput(t *testing.T) {
	if _, err := NewServiceProbe(uuid.Nil, ProbeKindRTT, "", 60, nil, nil); err == nil {
		t.Errorf("nil customer id should be rejected")
	}
	if _, err := NewServiceProbe(uuid.New(), ProbeKind("bogus"), "", 60, nil, nil); err == nil {
		t.Errorf("invalid kind should be rejected")
	}
}
