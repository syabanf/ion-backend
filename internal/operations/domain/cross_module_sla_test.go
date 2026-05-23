package domain

import (
	"testing"
	"time"
)

func TestRollup_AggregatesAndSortsTopBreachers(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	snaps := []SLASnapshot{
		{
			Module:        ModuleCS,
			AggregatedAt:  now.Add(-time.Minute),
			TotalAtRisk:   5,
			TotalBreached: 2,
			TopBreachers: []TopBreacherEntry{
				{Label: "T-1", MinutesLate: 30, Severity: "breached"},
				{Label: "T-2", MinutesLate: 10, Severity: "warn"},
			},
		},
		{
			Module:        ModuleField,
			AggregatedAt:  now,
			TotalAtRisk:   3,
			TotalBreached: 1,
			TopBreachers: []TopBreacherEntry{
				{Label: "WO-9", MinutesLate: 90, Severity: "breached"},
			},
		},
		{
			Module:        ModuleBilling,
			AggregatedAt:  now.Add(-2 * time.Minute),
			TotalAtRisk:   1,
			TotalBreached: 4,
			TopBreachers: []TopBreacherEntry{
				{Label: "INV-12", MinutesLate: 1440, Severity: "breached"},
			},
		},
	}

	out := Rollup(snaps, 2)
	if out.TotalAtRisk != 9 {
		t.Errorf("expected TotalAtRisk 9, got %d", out.TotalAtRisk)
	}
	if out.TotalBreached != 7 {
		t.Errorf("expected TotalBreached 7, got %d", out.TotalBreached)
	}
	if !out.AggregatedAt.Equal(now) {
		t.Errorf("expected AggregatedAt = newest snapshot timestamp")
	}
	if len(out.TopBreachersGlobal) != 2 {
		t.Fatalf("expected top 2 breachers, got %d", len(out.TopBreachersGlobal))
	}
	if out.TopBreachersGlobal[0].Label != "INV-12" {
		t.Errorf("expected highest minutes_late first; got %s", out.TopBreachersGlobal[0].Label)
	}
	if out.TopBreachersGlobal[1].Label != "WO-9" {
		t.Errorf("expected WO-9 second; got %s", out.TopBreachersGlobal[1].Label)
	}
}

func TestRollup_EmptyInput(t *testing.T) {
	out := Rollup(nil, 5)
	if out.TotalAtRisk != 0 || out.TotalBreached != 0 {
		t.Errorf("expected zeros for empty input")
	}
	if len(out.TopBreachersGlobal) != 0 {
		t.Errorf("expected empty TopBreachersGlobal")
	}
}
