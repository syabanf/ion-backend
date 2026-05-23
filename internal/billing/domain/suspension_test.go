// Wave 114 — Suspension state-machine table tests.

package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSuspensionNextActionFor(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	dueAgo := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	policy := DefaultSuspensionPolicy() // warn=7, soft=14, hard=21

	mk := func(daysPast int, last SuspensionActionKind) SuspensionEvalInput {
		return SuspensionEvalInput{
			CustomerID:         uuid.New(),
			OldestOverdueDue:   dueAgo(daysPast),
			HasOverdueInvoices: true,
			LastAction:         last,
		}
	}

	cases := []struct {
		name             string
		in               SuspensionEvalInput
		want             *SuspensionActionKind
		wantKindString   string
	}{
		{
			name: "no overdue invoices → nil",
			in:   SuspensionEvalInput{CustomerID: uuid.New()},
			want: nil,
		},
		{
			name:           "5 days past, no prior → nil (below warn=7)",
			in:             mk(5, ""),
			want:           nil,
		},
		{
			name:           "8 days past, no prior → warn",
			in:             mk(8, ""),
			wantKindString: string(SuspensionActionWarn),
		},
		{
			name:           "8 days past, already warned → nil",
			in:             mk(8, SuspensionActionWarn),
			want:           nil,
		},
		{
			name:           "15 days past, warned → soft_suspend",
			in:             mk(15, SuspensionActionWarn),
			wantKindString: string(SuspensionActionSoftSuspend),
		},
		{
			name:           "22 days past, soft suspended → hard_suspend",
			in:             mk(22, SuspensionActionSoftSuspend),
			wantKindString: string(SuspensionActionHardSuspend),
		},
		{
			name:           "downtime catch-up: 30 days past, no prior → jump to hard",
			in:             mk(30, ""),
			wantKindString: string(SuspensionActionHardSuspend),
		},
		{
			name: "already at hard_suspend → terminal, nil",
			in:   mk(30, SuspensionActionHardSuspend),
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.NextActionFor(tc.in, now)
			switch {
			case tc.want == nil && tc.wantKindString == "":
				if got != nil {
					t.Fatalf("want nil; got %q", *got)
				}
			default:
				if got == nil {
					t.Fatalf("want %q; got nil", tc.wantKindString)
				}
				if string(*got) != tc.wantKindString {
					t.Fatalf("want %q; got %q", tc.wantKindString, *got)
				}
			}
		})
	}
}

func TestShouldRestore(t *testing.T) {
	cases := []struct {
		name string
		in   RestoreEvalInput
		want bool
	}{
		{
			name: "soft suspend + no unpaid → restore",
			in: RestoreEvalInput{
				CurrentState:      CustomerSuspensionStateSoftSuspend,
				HasUnpaidInvoices: false,
			},
			want: true,
		},
		{
			name: "hard suspend + no unpaid → restore",
			in: RestoreEvalInput{
				CurrentState:      CustomerSuspensionStateHardSuspend,
				HasUnpaidInvoices: false,
			},
			want: true,
		},
		{
			name: "suspended but still owes → no restore",
			in: RestoreEvalInput{
				CurrentState:      CustomerSuspensionStateSoftSuspend,
				HasUnpaidInvoices: true,
			},
			want: false,
		},
		{
			name: "already active → no restore",
			in: RestoreEvalInput{
				CurrentState:      CustomerSuspensionStateActive,
				HasUnpaidInvoices: false,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRestore(tc.in); got != tc.want {
				t.Fatalf("ShouldRestore = %v; want %v", got, tc.want)
			}
		})
	}
}
