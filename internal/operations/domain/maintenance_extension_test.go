package domain

import (
	"testing"
	"time"
)

func TestApprovalRequired_BroadbandThreshold(t *testing.T) {
	cases := []struct {
		name     string
		count    int
		segment  CustomerSegment
		expected bool
	}{
		{"broadband 50 — no gate", 50, SegmentBroadband, false},
		{"broadband 100 — no gate", 100, SegmentBroadband, false},
		{"broadband 101 — gate", 101, SegmentBroadband, true},
		{"enterprise 50 — no gate", 50, SegmentEnterprise, false},
		{"enterprise 51 — gate", 51, SegmentEnterprise, true},
		{"mixed 51 — gate", 51, SegmentMixed, true},
		{"mixed 200 — gate", 200, SegmentMixed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApprovalRequired(tc.count, tc.segment); got != tc.expected {
				t.Fatalf("ApprovalRequired(%d, %s) = %v, want %v",
					tc.count, tc.segment, got, tc.expected)
			}
		})
	}
}

func TestLeadTimeHours(t *testing.T) {
	cases := []struct {
		segment CustomerSegment
		want    int
	}{
		{SegmentBroadband, 24},
		{SegmentEnterprise, 72},
		{SegmentMixed, 72},
		{CustomerSegment("unknown"), 24},
	}
	for _, tc := range cases {
		t.Run(string(tc.segment), func(t *testing.T) {
			if got := LeadTimeHours(tc.segment); got != tc.want {
				t.Fatalf("LeadTimeHours(%s) = %d, want %d", tc.segment, got, tc.want)
			}
		})
	}
}

func TestIsOverrun(t *testing.T) {
	end := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	tol := 30 * time.Minute

	cases := []struct {
		name   string
		end    *time.Time
		status string
		now    time.Time
		want   bool
	}{
		{
			"in progress 10 min after window — within tolerance",
			&end, "in_progress",
			end.Add(10 * time.Minute), false,
		},
		{
			"in progress 31 min after window — overrun",
			&end, "in_progress",
			end.Add(31 * time.Minute), true,
		},
		{
			"completed past window — not overrun",
			&end, "completed",
			end.Add(2 * time.Hour), false,
		},
		{
			"cancelled past window — not overrun",
			&end, "cancelled",
			end.Add(2 * time.Hour), false,
		},
		{
			"no scheduled end — not overrun",
			nil, "in_progress",
			end.Add(2 * time.Hour), false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOverrun(tc.end, tc.status, tc.now, tol); got != tc.want {
				t.Fatalf("IsOverrun = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNextLevel(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 4}, {5, 4}, {-1, 1},
	}
	for _, tc := range cases {
		if got := NextLevel(tc.in); got != tc.want {
			t.Errorf("NextLevel(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
