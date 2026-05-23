// Wave 114 — Reminder domain types.
//
// The reminder evaluator picks the next ReminderKind for an open
// invoice based on a schema-driven ReminderPolicy. The whole point of
// the policy living in domain (not the usecase) is so it can be unit-
// tested against table-driven cases without spinning up a pool / repo.
//
// ReminderKind has a strict monotonic order: once an invoice has had
// a 'soft_reminder' sent, the next eligible kind is 'due_today' (when
// the due date hits), then 'overdue_d1', etc. The evaluator never
// re-fires a kind — UNIQUE (invoice_id, kind) on billing.reminder_log
// is the durability anchor.

package domain

import (
	"time"

	"github.com/google/uuid"
)

// ReminderKind enumerates the points along an invoice's life where the
// scheduler may fan a reminder. The string values match the CHECK
// constraint on billing.reminder_log.kind.
type ReminderKind string

const (
	ReminderKindSoft               ReminderKind = "soft_reminder"
	ReminderKindDueToday           ReminderKind = "due_today"
	ReminderKindOverdueD1          ReminderKind = "overdue_d1"
	ReminderKindOverdueD3          ReminderKind = "overdue_d3"
	ReminderKindOverdueD7          ReminderKind = "overdue_d7"
	ReminderKindOverduePreSuspend  ReminderKind = "overdue_pre_suspend"
)

// reminderOrder is the canonical sequence the evaluator walks. Index
// position 0 is the earliest; higher indices are later in the life-
// cycle. Used internally by NextReminderKindForInvoice — we DO NOT
// expose the index to callers so we can reshuffle without breaking
// the API.
var reminderOrder = []ReminderKind{
	ReminderKindSoft,
	ReminderKindDueToday,
	ReminderKindOverdueD1,
	ReminderKindOverdueD3,
	ReminderKindOverdueD7,
	ReminderKindOverduePreSuspend,
}

// reminderIndex maps a kind to its position in reminderOrder; -1 if
// unknown (treated as "no prior reminder").
func reminderIndex(k ReminderKind) int {
	for i, r := range reminderOrder {
		if r == k {
			return i
		}
	}
	return -1
}

// ReminderPolicy is the resolved per-customer / per-schema config for
// the reminder cadence. Values flow in from the billing schema body
// via schema_policy.go; missing keys fall back to legacy defaults
// applied at the caller.
//
// SoftOffsetDaysBeforeDue — fire 'soft_reminder' when (now ≥ due - N).
//                           Zero or negative = no soft reminder.
// DueTodayAtHour          — hour-of-day (UTC) at which 'due_today'
//                           fires on the due date. Used only as a
//                           "don't fire before this hour" guard; the
//                           cron itself drives the cadence.
// OverdueSteps            — N-day milestones past the due date that
//                           map to 'overdue_d1' / 'overdue_d3' /
//                           'overdue_d7'. The evaluator picks the
//                           latest milestone the invoice has crossed.
// PreSuspendOffsetDays    — days before the suspension-evaluator's
//                           cutoff to fire 'overdue_pre_suspend' (the
//                           final-warning reminder). Defaults to 1.
// DefaultChannel          — 'whatsapp' | 'email' | 'sms'. Wired into
//                           the dispatcher call; can be overridden per
//                           OverdueStep.
type ReminderPolicy struct {
	SoftOffsetDaysBeforeDue int
	DueTodayAtHour          int
	OverdueSteps            []OverdueStep
	PreSuspendOffsetDays    int
	DefaultChannel          string
}

// OverdueStep maps an after-due offset (positive days) to a
// ReminderKind and optional per-step channel override.
type OverdueStep struct {
	DaysAfterDue int
	Kind         ReminderKind
	Channel      string // empty → use policy.DefaultChannel
}

// DefaultReminderPolicy returns the policy applied when the schema
// resolver returns nothing. Aligns with the TC-REM-001 default cadence
// (T-3, T-0, T+3, T+7) plus a pre-suspend final warning.
func DefaultReminderPolicy() ReminderPolicy {
	return ReminderPolicy{
		SoftOffsetDaysBeforeDue: 3,
		DueTodayAtHour:          9,
		OverdueSteps: []OverdueStep{
			{DaysAfterDue: 1, Kind: ReminderKindOverdueD1},
			{DaysAfterDue: 3, Kind: ReminderKindOverdueD3},
			{DaysAfterDue: 7, Kind: ReminderKindOverdueD7},
		},
		PreSuspendOffsetDays: 1,
		DefaultChannel:       "whatsapp",
	}
}

// ReminderEvalInput is the per-invoice projection the evaluator
// passes to NextReminderKindForInvoice. We keep it narrow — the
// domain function should not depend on the full InvoiceView.
type ReminderEvalInput struct {
	InvoiceID     uuid.UUID
	DueDate       time.Time
	IsPaid        bool
	IsCancelled   bool
	// LastSent is the latest ReminderKind already logged for this
	// invoice, or empty if none. Domain treats empty as "no prior".
	LastSent ReminderKind
	// SuspendAfterDays is the suspension policy's cutoff in days past
	// due; used to compute the 'overdue_pre_suspend' trigger window.
	// Zero means "don't fire pre_suspend".
	SuspendAfterDays int
}

// NextReminderKindForInvoice picks the next ReminderKind the evaluator
// should send for `in` at `now`, or nil if nothing is due.
//
// Rules (in order):
//
//	1. Paid or cancelled invoices never get reminders. Stop.
//	2. Pre-suspend window (now ≥ due + SuspendAfterDays -
//	   PreSuspendOffsetDays) fires 'overdue_pre_suspend' if not yet sent.
//	3. Find the highest overdue step the invoice has crossed; if past
//	   prior LastSent's index, return that kind.
//	4. If now is on or after due_date and 'due_today' hasn't fired yet,
//	   return 'due_today'.
//	5. If now ≥ due - SoftOffsetDaysBeforeDue and 'soft_reminder' not
//	   sent, return 'soft_reminder'.
//	6. Otherwise return nil — nothing to send right now.
//
// The "highest crossed step" rule keeps the evaluator safe across
// downtime: if the cron didn't run for two days, the next tick
// catches up by firing the latest applicable kind, not every kind in
// between (those windows are gone). UNIQUE (invoice_id, kind) keeps
// that safe — we never replay an already-sent kind.
func (p ReminderPolicy) NextReminderKindForInvoice(in ReminderEvalInput, now time.Time) *ReminderKind {
	if in.IsPaid || in.IsCancelled {
		return nil
	}
	lastIdx := reminderIndex(in.LastSent)
	due := in.DueDate

	// Rule 2: pre-suspend window.
	if in.SuspendAfterDays > 0 {
		preOffset := p.PreSuspendOffsetDays
		if preOffset <= 0 {
			preOffset = 1
		}
		preSuspendAt := due.AddDate(0, 0, in.SuspendAfterDays-preOffset)
		if !now.Before(preSuspendAt) {
			preIdx := reminderIndex(ReminderKindOverduePreSuspend)
			if lastIdx < preIdx {
				k := ReminderKindOverduePreSuspend
				return &k
			}
		}
	}

	// Rule 3: highest crossed overdue step.
	var candidate *ReminderKind
	candidateIdx := -1
	for _, step := range p.OverdueSteps {
		stepAt := due.AddDate(0, 0, step.DaysAfterDue)
		if !now.Before(stepAt) {
			idx := reminderIndex(step.Kind)
			if idx > candidateIdx {
				k := step.Kind
				candidate = &k
				candidateIdx = idx
			}
		}
	}
	if candidate != nil && candidateIdx > lastIdx {
		return candidate
	}

	// Rule 4: due_today.
	dueIdx := reminderIndex(ReminderKindDueToday)
	if !now.Before(due) && now.Before(due.AddDate(0, 0, 1)) && lastIdx < dueIdx {
		k := ReminderKindDueToday
		return &k
	}

	// Rule 5: soft reminder.
	if p.SoftOffsetDaysBeforeDue > 0 {
		softAt := due.AddDate(0, 0, -p.SoftOffsetDaysBeforeDue)
		softIdx := reminderIndex(ReminderKindSoft)
		if !now.Before(softAt) && now.Before(due) && lastIdx < softIdx {
			k := ReminderKindSoft
			return &k
		}
	}

	return nil
}

// ChannelFor returns the channel to use for a given kind, falling back
// to DefaultChannel when no per-step override is set.
func (p ReminderPolicy) ChannelFor(k ReminderKind) string {
	for _, step := range p.OverdueSteps {
		if step.Kind == k && step.Channel != "" {
			return step.Channel
		}
	}
	if p.DefaultChannel != "" {
		return p.DefaultChannel
	}
	return "whatsapp"
}
