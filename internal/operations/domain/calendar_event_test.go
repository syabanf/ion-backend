package domain

import (
	"testing"
	"time"
)

func TestCalendarEvent_IsActiveAt(t *testing.T) {
	start := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)

	e := &CalendarEvent{StartsAt: start, EndsAt: &end}
	if e.IsActiveAt(start.Add(-time.Minute)) {
		t.Errorf("before start should be inactive")
	}
	if !e.IsActiveAt(start.Add(time.Minute)) {
		t.Errorf("within window should be active")
	}
	if !e.IsActiveAt(end) {
		t.Errorf("at end should still be active (inclusive)")
	}
	if e.IsActiveAt(end.Add(time.Minute)) {
		t.Errorf("past end should be inactive")
	}
}

func TestCalendarEvent_IsActiveAt_OpenEnded(t *testing.T) {
	start := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	e := &CalendarEvent{StartsAt: start, EndsAt: nil}
	if !e.IsActiveAt(start.Add(48 * time.Hour)) {
		t.Errorf("open-ended event should remain active")
	}
}

func TestCalendarEvent_OverlapsRange(t *testing.T) {
	start := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	e := &CalendarEvent{StartsAt: start, EndsAt: &end}

	if !e.OverlapsRange(start.Add(-time.Hour), start.Add(time.Hour)) {
		t.Errorf("should overlap when range straddles start")
	}
	if e.OverlapsRange(end.Add(time.Hour), end.Add(2*time.Hour)) {
		t.Errorf("should not overlap when range starts after event end")
	}
	if e.OverlapsRange(start.Add(-3*time.Hour), start.Add(-time.Hour)) {
		t.Errorf("should not overlap when range ends before event start")
	}
}

func TestNormalizeEventKind(t *testing.T) {
	if NormalizeEventKind("MAINTENANCE") != EventKindMaintenance {
		t.Errorf("expected maintenance")
	}
	if NormalizeEventKind("garbage") != EventKindCustom {
		t.Errorf("expected custom fallback")
	}
}
