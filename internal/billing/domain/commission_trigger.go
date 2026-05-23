// Wave 114 — Commission trigger policy.
//
// The commission-trigger evaluator scans recently-paid invoices that
// tie back to a plan_change_id (via crm.plan_change_requests) with a
// sales_user_id and asks: "given the resolved commission schema, what
// trigger kind (if any) should fire?" The answer becomes a
// billing.commission_triggers row that a downstream worker (out of
// Wave 114's scope) consumes into the actual commission_records
// ledger.
//
// Distinct from the existing OTC-on-paid commission split (r2.go's
// maybeApplyCommission) which fires the 5-party split directly. Wave
// 114 is about the *trigger-config* layer — letting ops swap when
// commission lands (on_paid vs on_activated vs on_anniversary) without
// changing service code.

package domain

import (
	"time"

	"github.com/google/uuid"
)

// CommissionTriggerKind enumerates the moments at which a commission
// row may be queued. Mirrors the CHECK on
// billing.commission_triggers.trigger_kind.
type CommissionTriggerKind string

const (
	CommissionTriggerOnPaid        CommissionTriggerKind = "on_paid"
	CommissionTriggerOnActivated   CommissionTriggerKind = "on_activated"
	CommissionTriggerOnAnniversary CommissionTriggerKind = "on_anniversary"
	CommissionTriggerManual        CommissionTriggerKind = "manual"
)

// CommissionRecipientKind tags who gets the commission row when the
// trigger fires. The runtime resolution to a concrete user_id lives
// in the caller (it requires walking reports_to / branch membership)
// — this is just the abstract category coming out of the schema.
type CommissionRecipientKind string

const (
	CommissionRecipientSales       CommissionRecipientKind = "sales"
	CommissionRecipientSalesLead   CommissionRecipientKind = "sales_lead"
	CommissionRecipientSalesBranch CommissionRecipientKind = "sales_branch"
)

// CommissionTriggerPolicy is the resolved per-customer / per-schema
// configuration for commission triggers.
//
// Trigger             — when to fire (on_paid is the default; older
//                       deployments fired commission only at
//                       activation).
// Recipient           — who the commission lands on.
// ClawbackDays        — if the customer terminates within N days
//                       after the trigger fires, the row is clawed
//                       back. Zero/negative = no clawback.
// PercentageOfBasis   — when set, the trigger row's commission_amount
//                       is computed as amount_basis * pct / 100. The
//                       basis is supplied by the caller (invoice
//                       total, monthly recurring, etc.).
// FlatAmount          — when set and PercentageOfBasis is 0, a fixed
//                       IDR amount is queued.
type CommissionTriggerPolicy struct {
	Trigger           CommissionTriggerKind
	Recipient         CommissionRecipientKind
	ClawbackDays      int
	PercentageOfBasis float64
	FlatAmount        float64
}

// DefaultCommissionTriggerPolicy mirrors the historical behaviour the
// service had pre-Wave-114: on first payment, 15% goes to the sales
// rep. Used when neither the schema nor a custom override supplies
// anything.
func DefaultCommissionTriggerPolicy() CommissionTriggerPolicy {
	return CommissionTriggerPolicy{
		Trigger:           CommissionTriggerOnPaid,
		Recipient:         CommissionRecipientSales,
		ClawbackDays:      90,
		PercentageOfBasis: 15,
	}
}

// CommissionTriggerInput is the per-invoice projection used by
// EvaluateTrigger. The caller pre-resolves whether the invoice is
// paid + tied to a plan change with a sales user — the domain helper
// just decides "given the policy, what row would we queue?"
type CommissionTriggerInput struct {
	InvoiceID     uuid.UUID
	CustomerID    uuid.UUID
	PlanChangeID  *uuid.UUID
	SalesUserID   uuid.UUID
	AmountBasis   float64
	PaidAt        *time.Time
	ActivatedAt   *time.Time
	AnniversaryAt *time.Time
}

// EvaluatedTrigger is the projection returned by EvaluateTrigger when
// a trigger fires. Maps almost 1:1 to billing.commission_triggers
// columns — the caller writes the row.
type EvaluatedTrigger struct {
	PlanChangeID      *uuid.UUID
	CustomerID        uuid.UUID
	SalesUserID       uuid.UUID
	TriggerKind       CommissionTriggerKind
	InvoiceID         *uuid.UUID
	AmountBasis       float64
	CommissionAmount  float64
	Recipient         CommissionRecipientKind
}

// EvaluateTrigger returns the EvaluatedTrigger that should be queued
// for the input at `now`, or nil if nothing fires. Rules:
//
//   * on_paid       — fires when PaidAt != nil AND PaidAt ≤ now.
//   * on_activated  — fires when ActivatedAt != nil AND ActivatedAt ≤ now.
//   * on_anniversary— fires when AnniversaryAt != nil AND
//                     AnniversaryAt ≤ now (caller computes the
//                     anniversary date; domain doesn't recompute it).
//   * manual        — never fires from the cron; the trigger row is
//                     written by an admin action.
//
// Caller is responsible for the per-plan-change idempotency check
// (UNIQUE on billing.commission_triggers (plan_change_id,
// trigger_kind)). This helper is purely about "should we ever
// queue?".
func (p CommissionTriggerPolicy) EvaluateTrigger(in CommissionTriggerInput, now time.Time) *EvaluatedTrigger {
	if p.Trigger == CommissionTriggerManual {
		return nil
	}
	if in.SalesUserID == uuid.Nil {
		return nil
	}

	var fire bool
	switch p.Trigger {
	case CommissionTriggerOnPaid:
		fire = in.PaidAt != nil && !in.PaidAt.After(now)
	case CommissionTriggerOnActivated:
		fire = in.ActivatedAt != nil && !in.ActivatedAt.After(now)
	case CommissionTriggerOnAnniversary:
		fire = in.AnniversaryAt != nil && !in.AnniversaryAt.After(now)
	}
	if !fire {
		return nil
	}

	commissionAmount := 0.0
	if p.PercentageOfBasis > 0 {
		commissionAmount = round2Commission(in.AmountBasis * p.PercentageOfBasis / 100.0)
	} else if p.FlatAmount > 0 {
		commissionAmount = round2Commission(p.FlatAmount)
	}

	out := &EvaluatedTrigger{
		PlanChangeID:     in.PlanChangeID,
		CustomerID:       in.CustomerID,
		SalesUserID:      in.SalesUserID,
		TriggerKind:      p.Trigger,
		AmountBasis:      in.AmountBasis,
		CommissionAmount: commissionAmount,
		Recipient:        p.Recipient,
	}
	if in.InvoiceID != uuid.Nil {
		out.InvoiceID = &in.InvoiceID
	}
	return out
}

// round2Commission mirrors the rounding used elsewhere in the domain.
func round2Commission(f float64) float64 {
	if f >= 0 {
		return float64(int64(f*100+0.5)) / 100
	}
	return float64(int64(f*100-0.5)) / 100
}
