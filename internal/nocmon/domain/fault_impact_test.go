package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestComputeSLACreditEligible(t *testing.T) {
	cases := []struct {
		dur     float64
		window  float64
		want    bool
		desc    string
	}{
		{2.0, 4.0, false, "shorter than window"},
		{4.0, 4.0, true, "at the window boundary"},
		{6.0, 4.0, true, "longer than window"},
		{6.0, 0.0, false, "disabled window"},
		{6.0, -1.0, false, "negative window"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := ComputeSLACreditEligible(tc.dur, tc.window)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestNewFaultImpactValidates(t *testing.T) {
	now := time.Now().UTC()
	if _, err := NewFaultImpact(uuid.Nil, uuid.New(), ImpactKindFullOutage, now, false); err == nil {
		t.Errorf("nil fault id should be rejected")
	}
	if _, err := NewFaultImpact(uuid.New(), uuid.Nil, ImpactKindFullOutage, now, false); err == nil {
		t.Errorf("nil customer id should be rejected")
	}
	if _, err := NewFaultImpact(uuid.New(), uuid.New(), ImpactKind("bogus"), now, false); err == nil {
		t.Errorf("invalid impact kind should be rejected")
	}
	imp, err := NewFaultImpact(uuid.New(), uuid.New(), ImpactKindFullOutage, now, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !imp.SLACreditEligible {
		t.Errorf("eligible flag should round-trip")
	}
}
