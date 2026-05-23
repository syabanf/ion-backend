// Wave 114 — Late fee policy table tests.
//
// Locks in IsEligible + Compute against the TC-LF-* objectives:
// percentage vs flat, cap, grace, disabled (corporate exemption).

package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLateFeeIsEligible(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	dueAgo := func(days int) time.Time { return now.AddDate(0, 0, -days) }

	policy := LateFeePolicy{
		FlatAmount: 25000,
		GraceDays:  3,
	}

	cases := []struct {
		name string
		in   LateFeeEvalInput
		want bool
	}{
		{
			name: "within grace → not eligible",
			in:   LateFeeEvalInput{DueDate: dueAgo(2), OutstandingAmount: 100000},
			want: false,
		},
		{
			name: "exactly at grace cutoff → eligible (boundary)",
			in:   LateFeeEvalInput{DueDate: dueAgo(3), OutstandingAmount: 100000},
			want: true,
		},
		{
			name: "past grace, has outstanding → eligible",
			in:   LateFeeEvalInput{DueDate: dueAgo(7), OutstandingAmount: 100000},
			want: true,
		},
		{
			name: "paid → never eligible",
			in:   LateFeeEvalInput{DueDate: dueAgo(7), IsPaid: true, OutstandingAmount: 0},
			want: false,
		},
		{
			name: "cancelled → never eligible",
			in:   LateFeeEvalInput{DueDate: dueAgo(7), IsCancelled: true, OutstandingAmount: 100000},
			want: false,
		},
		{
			name: "zero outstanding → not eligible",
			in:   LateFeeEvalInput{DueDate: dueAgo(7), OutstandingAmount: 0},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.InvoiceID = uuid.New()
			if got := policy.IsEligible(tc.in, now); got != tc.want {
				t.Fatalf("IsEligible = %v; want %v", got, tc.want)
			}
		})
	}

	t.Run("disabled policy → never eligible", func(t *testing.T) {
		p := policy
		p.Disabled = true
		in := LateFeeEvalInput{DueDate: dueAgo(7), OutstandingAmount: 100000}
		if p.IsEligible(in, now) {
			t.Fatal("disabled policy must always reject")
		}
	})
}

func TestLateFeeCompute(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	dueAgo := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	base := LateFeeEvalInput{DueDate: dueAgo(7), OutstandingAmount: 200000}

	t.Run("flat fee", func(t *testing.T) {
		p := LateFeePolicy{FlatAmount: 25000, GraceDays: 3}
		if got := p.Compute(base, now); got != 25000 {
			t.Fatalf("flat fee Compute = %v; want 25000", got)
		}
	})

	t.Run("percentage fee", func(t *testing.T) {
		p := LateFeePolicy{PercentageOfOutstanding: 5, GraceDays: 3}
		// 5% of 200000 = 10000
		if got := p.Compute(base, now); got != 10000 {
			t.Fatalf("percentage Compute = %v; want 10000", got)
		}
	})

	t.Run("percentage with cap", func(t *testing.T) {
		// 10% of 1,000,000 = 100,000; cap at 50,000.
		p := LateFeePolicy{PercentageOfOutstanding: 10, CapAmount: 50000, GraceDays: 3}
		in := base
		in.OutstandingAmount = 1000000
		if got := p.Compute(in, now); got != 50000 {
			t.Fatalf("capped Compute = %v; want 50000", got)
		}
	})

	t.Run("percentage takes precedence over flat", func(t *testing.T) {
		p := LateFeePolicy{FlatAmount: 25000, PercentageOfOutstanding: 5, GraceDays: 3}
		// 5% of 200000 = 10000, NOT the flat 25000.
		if got := p.Compute(base, now); got != 10000 {
			t.Fatalf("percent precedence Compute = %v; want 10000", got)
		}
	})

	t.Run("within grace → 0", func(t *testing.T) {
		p := LateFeePolicy{FlatAmount: 25000, GraceDays: 3}
		in := LateFeeEvalInput{DueDate: dueAgo(1), OutstandingAmount: 200000}
		if got := p.Compute(in, now); got != 0 {
			t.Fatalf("within grace Compute = %v; want 0", got)
		}
	})

	t.Run("disabled → 0", func(t *testing.T) {
		p := LateFeePolicy{FlatAmount: 25000, GraceDays: 3, Disabled: true}
		if got := p.Compute(base, now); got != 0 {
			t.Fatalf("disabled Compute = %v; want 0", got)
		}
	})
}
