// Wave 114 — Reminder policy table tests.
//
// Locks in the NextReminderKindForInvoice state machine against the
// TC-REM-* objectives: T-3 soft, T-0 due_today, T+1/3/7 overdue, and
// the pre-suspend final warning. The tests exercise the
// "downtime-catch-up" path (cron didn't run for 2 days → next tick
// fires the latest applicable kind, not every intermediate kind).

package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNextReminderKindForInvoice(t *testing.T) {
	policy := DefaultReminderPolicy()
	// Anchor due date — Apr 10 2026 (a Friday).
	due := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	at := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 12, 0, 0, 0, time.UTC)
	}
	mk := func(now time.Time, last ReminderKind, suspendAfter int) ReminderEvalInput {
		return ReminderEvalInput{
			InvoiceID:        uuid.New(),
			DueDate:          due,
			LastSent:         last,
			SuspendAfterDays: suspendAfter,
		}
	}

	cases := []struct {
		name             string
		in               ReminderEvalInput
		now              time.Time
		want             *ReminderKind
		wantKindString   string
	}{
		{
			name:           "no prior, 5 days before due → nothing",
			in:             mk(at(2026, 4, 5), "", 14),
			now:            at(2026, 4, 5),
			want:           nil,
		},
		{
			name:           "no prior, T-3 → soft_reminder",
			in:             mk(at(2026, 4, 7), "", 14),
			now:            at(2026, 4, 7),
			wantKindString: string(ReminderKindSoft),
		},
		{
			name:           "soft already fired, T-3 → nothing more",
			in:             mk(at(2026, 4, 7), ReminderKindSoft, 14),
			now:            at(2026, 4, 7),
			want:           nil,
		},
		{
			name:           "soft already fired, due date → due_today",
			in:             mk(at(2026, 4, 10), ReminderKindSoft, 14),
			now:            at(2026, 4, 10),
			wantKindString: string(ReminderKindDueToday),
		},
		{
			name:           "no prior, due date → soft + due skipped → due_today wins (later step)",
			in:             mk(at(2026, 4, 10), "", 14),
			now:            at(2026, 4, 10),
			wantKindString: string(ReminderKindDueToday),
		},
		{
			name:           "due_today fired, +1 day → overdue_d1",
			in:             mk(at(2026, 4, 11), ReminderKindDueToday, 14),
			now:            at(2026, 4, 11),
			wantKindString: string(ReminderKindOverdueD1),
		},
		{
			name:           "overdue_d1 fired, +3 days → overdue_d3",
			in:             mk(at(2026, 4, 13), ReminderKindOverdueD1, 14),
			now:            at(2026, 4, 13),
			wantKindString: string(ReminderKindOverdueD3),
		},
		{
			name:           "downtime catch-up: nothing fired, +7 days → jump to overdue_d7",
			in:             mk(at(2026, 4, 17), "", 14),
			now:            at(2026, 4, 17),
			wantKindString: string(ReminderKindOverdueD7),
		},
		{
			name:           "pre-suspend window: +13 days (1 day before suspend=14)",
			in:             mk(at(2026, 4, 23), ReminderKindOverdueD7, 14),
			now:            at(2026, 4, 23),
			wantKindString: string(ReminderKindOverduePreSuspend),
		},
		{
			name:           "paid invoice → no reminder regardless",
			in: ReminderEvalInput{
				InvoiceID:        uuid.New(),
				DueDate:          due,
				IsPaid:           true,
				LastSent:         ReminderKindSoft,
				SuspendAfterDays: 14,
			},
			now:  at(2026, 4, 20),
			want: nil,
		},
		{
			name:           "cancelled invoice → no reminder",
			in: ReminderEvalInput{
				InvoiceID:   uuid.New(),
				DueDate:     due,
				IsCancelled: true,
				LastSent:    "",
			},
			now:  at(2026, 4, 11),
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.NextReminderKindForInvoice(tc.in, tc.now)
			switch {
			case tc.want == nil && tc.wantKindString == "":
				if got != nil {
					t.Fatalf("want nil; got %q", *got)
				}
			case tc.wantKindString != "":
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

func TestReminderChannelFor(t *testing.T) {
	policy := DefaultReminderPolicy()
	if got := policy.ChannelFor(ReminderKindSoft); got != "whatsapp" {
		t.Fatalf("ChannelFor(soft) = %q; want whatsapp (DefaultChannel)", got)
	}
	// Override per-step.
	policy.OverdueSteps[2].Channel = "sms"
	if got := policy.ChannelFor(policy.OverdueSteps[2].Kind); got != "sms" {
		t.Fatalf("per-step override not honored; got %q", got)
	}
}
