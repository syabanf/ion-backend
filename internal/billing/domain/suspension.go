// Wave 114 — Suspension state-machine policy.
//
// The suspension evaluator walks every customer with overdue invoices
// and asks: "given the resolved suspension schema, what's the next
// action (if any)?" The answer is one of {warn, soft_suspend,
// hard_suspend, restore} or nil for "stay put". The state machine is
// strictly forward-only:
//
//	none → warn → soft_suspend → hard_suspend → restore → (back to none)
//
// 'restore' is special — it's emitted by the *restore-on-paid* cron,
// not the suspension evaluator. We keep the kind here for parity so
// the same domain helper can audit-validate the action sequence.

package domain

import (
	"time"

	"github.com/google/uuid"
)

// CustomerSuspensionState is the public-facing state the bridge
// adapter syncs into crm.customers when an action lands. The string
// values are the same the CRM gateway already expects.
type CustomerSuspensionState string

const (
	CustomerSuspensionStateActive       CustomerSuspensionState = "active"
	CustomerSuspensionStateSoftSuspend  CustomerSuspensionState = "soft_suspend"
	CustomerSuspensionStateHardSuspend  CustomerSuspensionState = "hard_suspend"
)

// SuspensionActionKind enumerates the cron-emitted actions. Mirrors
// the CHECK on billing.suspension_actions.action.
type SuspensionActionKind string

const (
	SuspensionActionWarn         SuspensionActionKind = "warn"
	SuspensionActionSoftSuspend  SuspensionActionKind = "soft_suspend"
	SuspensionActionHardSuspend  SuspensionActionKind = "hard_suspend"
	SuspensionActionRestore      SuspensionActionKind = "restore"
)

// suspensionEscalationOrder is the canonical forward-only sequence.
// Index 0 = earliest (warn); higher = later. -1 reserved for "no prior".
var suspensionEscalationOrder = []SuspensionActionKind{
	SuspensionActionWarn,
	SuspensionActionSoftSuspend,
	SuspensionActionHardSuspend,
}

func suspensionEscalationIndex(k SuspensionActionKind) int {
	for i, a := range suspensionEscalationOrder {
		if a == k {
			return i
		}
	}
	return -1
}

// SuspensionPolicy is the resolved per-customer / per-schema config
// for the suspension cadence.
//
// GraceDaysBeforeWarn          — days past due before the warn-
//                                 dispatch fires. Zero = warn at due+0.
// GraceDaysBeforeSoftSuspend   — days past due before soft suspend.
//                                 Must be > BeforeWarn or the warn
//                                 stage is skipped (still valid; some
//                                 schemas omit the warn pass).
// GraceDaysBeforeHardSuspend   — days past due before hard suspend.
//                                 Must be ≥ BeforeSoftSuspend.
// RequiresSupervisorForHardSuspend
//                              — corporate / enterprise toggle. When
//                                 true the cron only writes a
//                                 suspension_actions row marking it
//                                 'pending'; the supervisor approval
//                                 path (out of Wave 114's scope) is
//                                 responsible for the actual flip.
//                                 The evaluator still emits the
//                                 NextActionFor result so the caller
//                                 can fan to an approvals queue.
type SuspensionPolicy struct {
	GraceDaysBeforeWarn               int
	GraceDaysBeforeSoftSuspend        int
	GraceDaysBeforeHardSuspend        int
	RequiresSupervisorForHardSuspend  bool
}

// DefaultSuspensionPolicy mirrors the legacy billing.policies row's
// suspend_after_days=14 / terminate_after_suspended_days=30 cadence,
// reframed in the warn → soft → hard ladder. A green-field install
// gets this; schema overrides can tighten or relax it per customer.
func DefaultSuspensionPolicy() SuspensionPolicy {
	return SuspensionPolicy{
		GraceDaysBeforeWarn:        7,
		GraceDaysBeforeSoftSuspend: 14,
		GraceDaysBeforeHardSuspend: 21,
	}
}

// SuspensionEvalInput is the per-customer projection. The evaluator
// loops over candidate customers, builds this struct from their
// oldest-overdue invoice's due_date + the latest action already in
// billing.suspension_actions, and asks NextActionFor for the next
// move.
type SuspensionEvalInput struct {
	CustomerID         uuid.UUID
	OldestOverdueDue   time.Time
	HasOverdueInvoices bool
	// LastAction is the latest cron-emitted SuspensionActionKind for
	// this customer, or empty if none. Domain treats empty as "no
	// prior action; eligible for warn".
	LastAction SuspensionActionKind
}

// NextActionFor picks the next SuspensionActionKind to take for the
// customer at `now`, or nil if nothing escalates. Rules:
//
//   1. No overdue invoices → nil (the restore pass handles that branch
//      via RestoreEvalInput).
//   2. Compute the next stage based on days-past-due thresholds. If
//      the customer has already had a 'hard_suspend' action, no
//      further escalation is possible (still in hard suspend until
//      restore fires).
//   3. Return the highest crossed stage that's strictly newer than
//      LastAction. This keeps the cron safe across downtime (catches
//      up by jumping to the latest applicable stage).
//
// Restore is NOT emitted here — that's the restore-on-paid cron's
// job. NextActionFor only escalates.
func (p SuspensionPolicy) NextActionFor(in SuspensionEvalInput, now time.Time) *SuspensionActionKind {
	if !in.HasOverdueInvoices {
		return nil
	}
	lastIdx := suspensionEscalationIndex(in.LastAction)
	// Already at the terminal escalation stage — nothing to do.
	if in.LastAction == SuspensionActionHardSuspend {
		return nil
	}

	daysPast := int(now.Sub(in.OldestOverdueDue).Hours() / 24)

	// Walk stages from most-severe to least; first matching wins so a
	// long-overdue customer skips straight to hard_suspend if the
	// cron has been down.
	type stageDef struct {
		kind  SuspensionActionKind
		grace int
	}
	stages := []stageDef{
		{SuspensionActionHardSuspend, p.GraceDaysBeforeHardSuspend},
		{SuspensionActionSoftSuspend, p.GraceDaysBeforeSoftSuspend},
		{SuspensionActionWarn, p.GraceDaysBeforeWarn},
	}
	for _, st := range stages {
		if st.grace <= 0 && st.kind != SuspensionActionWarn {
			continue // schema omitted this stage
		}
		if daysPast >= st.grace {
			idx := suspensionEscalationIndex(st.kind)
			if idx > lastIdx {
				k := st.kind
				return &k
			}
			// This stage already fired; no escalation possible (since
			// stages walk severity-down, lower stages can't be newer
			// either).
			return nil
		}
	}
	return nil
}

// RestoreEvalInput is the per-customer projection the restore-on-paid
// cron uses. The evaluator finds customers whose suspension state is
// soft_suspend or hard_suspend AND who have no remaining unpaid
// invoices, and flips them back to active.
type RestoreEvalInput struct {
	CustomerID            uuid.UUID
	CurrentState          CustomerSuspensionState
	HasUnpaidInvoices     bool
	HasPaidInLastNMinutes bool // optional reactivity hint — within e.g. 5 min
}

// ShouldRestore reports whether the customer is eligible to flip back
// to active. The cron should only call this when CurrentState is one
// of the suspended variants — Active customers never need restore.
func (RestoreEvalInput) ShouldRestore(in RestoreEvalInput) bool {
	switch in.CurrentState {
	case CustomerSuspensionStateSoftSuspend, CustomerSuspensionStateHardSuspend:
		return !in.HasUnpaidInvoices
	}
	return false
}

// ShouldRestore is the package-level helper exposed for tests; mirrors
// the receiver method above (kept as both forms so callers without a
// zero RestoreEvalInput can still call it).
func ShouldRestore(in RestoreEvalInput) bool {
	return RestoreEvalInput{}.ShouldRestore(in)
}
