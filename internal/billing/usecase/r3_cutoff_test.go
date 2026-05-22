// Locks in the auto-termination cutoff predicate. The E2E suite hits
// the path live (suspending a customer, backdating suspended_at past
// the threshold, then triggering a tick), but exercises the boundary
// only roughly. This table lives next to the predicate so accidental
// off-by-one changes break a focused unit test rather than the
// integration test where the failure mode is fuzzier.
package usecase

import (
	"testing"
	"time"
)

func TestShouldAutoTerminate(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	dayBefore := func(d int) *time.Time {
		t := now.AddDate(0, 0, -d)
		return &t
	}

	cases := []struct {
		name        string
		suspendedAt *time.Time
		threshold   int
		want        bool
	}{
		{
			name:        "never suspended",
			suspendedAt: nil,
			threshold:   30,
			want:        false,
		},
		{
			name:        "suspended same instant, threshold 0 days",
			suspendedAt: &now,
			threshold:   0,
			want:        true, // cutoff == suspendedAt → eligible
		},
		{
			name:        "suspended one second after the cutoff",
			suspendedAt: func() *time.Time { x := now.AddDate(0, 0, -30).Add(time.Second); return &x }(),
			threshold:   30,
			want:        false, // still inside the 30-day window
		},
		{
			name:        "suspended exactly at the cutoff",
			suspendedAt: dayBefore(30),
			threshold:   30,
			want:        true, // boundary is eligible
		},
		{
			name:        "suspended one second before the cutoff",
			suspendedAt: func() *time.Time { x := now.AddDate(0, 0, -30).Add(-time.Second); return &x }(),
			threshold:   30,
			want:        true,
		},
		{
			name:        "suspended long ago",
			suspendedAt: dayBefore(90),
			threshold:   30,
			want:        true,
		},
		{
			name:        "future-dated suspension under threshold 0",
			suspendedAt: func() *time.Time { x := now.Add(time.Hour); return &x }(),
			threshold:   0,
			want:        false, // suspendedAt > cutoff(=now), skip
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAutoTerminate(now, tc.suspendedAt, tc.threshold)
			if got != tc.want {
				t.Fatalf("shouldAutoTerminate(now, %v, %d) = %v, want %v",
					tc.suspendedAt, tc.threshold, got, tc.want)
			}
		})
	}
}
