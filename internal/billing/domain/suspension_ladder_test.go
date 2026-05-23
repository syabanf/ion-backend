// Wave 120 — full warn → soft → hard escalation ladder.
//
// Pins TC-SUS-* / TC-SUE-* "a customer with progressively-older
// overdue invoices must escalate through warn (day 7) → soft_suspend
// (day 14) → hard_suspend (day 21) and then stay at hard_suspend
// indefinitely (no further escalation)". Each tick of the suspension
// cron calls NextActionFor with the LATEST action already in the
// audit log; the domain decides what to do next.

package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSuspensionPolicy_FullLadder_WarnSoftHard(t *testing.T) {
	policy := DefaultSuspensionPolicy() // 7/14/21
	custID := uuid.New()
	due := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	type step struct {
		atDays     int
		lastAction SuspensionActionKind
		want       SuspensionActionKind // empty → expect nil
		desc       string
	}
	steps := []step{
		{atDays: 0, lastAction: "", want: "", desc: "day 0 — no overdue effect"},
		{atDays: 7, lastAction: "", want: SuspensionActionWarn, desc: "day 7 — warn fires"},
		{atDays: 8, lastAction: SuspensionActionWarn, want: "", desc: "day 8 — no re-warn"},
		{atDays: 14, lastAction: SuspensionActionWarn, want: SuspensionActionSoftSuspend, desc: "day 14 — soft suspend"},
		{atDays: 20, lastAction: SuspensionActionSoftSuspend, want: "", desc: "day 20 — between soft and hard"},
		{atDays: 21, lastAction: SuspensionActionSoftSuspend, want: SuspensionActionHardSuspend, desc: "day 21 — hard suspend"},
		{atDays: 30, lastAction: SuspensionActionHardSuspend, want: "", desc: "day 30 — already at hard, no further escalation"},
		{atDays: 90, lastAction: SuspensionActionHardSuspend, want: "", desc: "day 90 — still no further escalation"},
	}

	for _, s := range steps {
		t.Run(s.desc, func(t *testing.T) {
			now := due.AddDate(0, 0, s.atDays)
			next := policy.NextActionFor(SuspensionEvalInput{
				CustomerID:         custID,
				OldestOverdueDue:   due,
				HasOverdueInvoices: true,
				LastAction:         s.lastAction,
			}, now)
			if s.want == "" {
				if next != nil {
					t.Errorf("want nil; got %s", *next)
				}
				return
			}
			if next == nil {
				t.Fatalf("want %s; got nil", s.want)
			}
			if *next != s.want {
				t.Errorf("want %s; got %s", s.want, *next)
			}
		})
	}
}

func TestSuspensionPolicy_CronCatchup_JumpsStraightToHard(t *testing.T) {
	// Cron was down for 30 days; on resume the customer is past all
	// three gates. NextActionFor should pick HardSuspend (the most
	// severe applicable stage) not Warn (the oldest).
	policy := DefaultSuspensionPolicy()
	due := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	now := due.AddDate(0, 0, 45) // 45 days past — well past hard

	next := policy.NextActionFor(SuspensionEvalInput{
		CustomerID:         uuid.New(),
		OldestOverdueDue:   due,
		HasOverdueInvoices: true,
		LastAction:         "", // nothing fired during the outage
	}, now)
	if next == nil {
		t.Fatalf("want hard_suspend; got nil")
	}
	if *next != SuspensionActionHardSuspend {
		t.Errorf("want hard_suspend; got %s", *next)
	}
}

func TestSuspensionPolicy_RestoredCustomer_NoActionWhenNotOverdue(t *testing.T) {
	policy := DefaultSuspensionPolicy()
	now := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	// Even with a stale LastAction = hard_suspend, if HasOverdueInvoices
	// is false the evaluator returns nil (the restore-on-paid cron
	// drives the restoration, not NextActionFor).
	next := policy.NextActionFor(SuspensionEvalInput{
		CustomerID:         uuid.New(),
		HasOverdueInvoices: false,
		LastAction:         SuspensionActionHardSuspend,
	}, now)
	if next != nil {
		t.Errorf("want nil for no-overdue customer; got %v", *next)
	}
}

func TestSuspensionPolicy_SchemaOmitsWarn_GoesStraightToSoft(t *testing.T) {
	// A schema with GraceDaysBeforeWarn == 0 (and so does the helper
	// "stages with grace <= 0 are skipped except warn") is supposed to
	// still emit warn at days 0. Let's check the specific case of a
	// "no warn" schema with high BeforeSoftSuspend.
	policy := SuspensionPolicy{
		GraceDaysBeforeWarn:        0, // 'warn' always considered (per loop guard)
		GraceDaysBeforeSoftSuspend: 10,
		GraceDaysBeforeHardSuspend: 30,
	}
	due := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	now := due.AddDate(0, 0, 12) // past soft, before hard

	next := policy.NextActionFor(SuspensionEvalInput{
		CustomerID:         uuid.New(),
		OldestOverdueDue:   due,
		HasOverdueInvoices: true,
		LastAction:         "",
	}, now)
	if next == nil {
		t.Fatalf("want soft_suspend; got nil")
	}
	if *next != SuspensionActionSoftSuspend {
		t.Errorf("want soft_suspend; got %s", *next)
	}
}
