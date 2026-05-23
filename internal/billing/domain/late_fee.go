// Wave 114 — Late fee domain types.
//
// The late-fee evaluator computes ONE per-invoice fee when an invoice
// crosses (due_date + grace_days) and writes it to
// billing.late_fee_applications. UNIQUE (invoice_id) keeps the cron
// idempotent — subsequent re-runs no-op via ON CONFLICT DO NOTHING.
//
// LateFeePolicy is the schema-driven projection of that math; the
// usecase reads schema body keys and populates it before calling
// Compute/IsEligible.

package domain

import (
	"time"

	"github.com/google/uuid"
)

// LateFeePolicy captures the per-customer / per-schema late-fee
// configuration. Values flow in from the billing schema body via
// schema_policy.go; missing keys fall back to legacy defaults applied
// at the caller (e.g. flat IDR 25000 from billing.policies).
//
// FlatAmount               — fixed IDR fee. Used when
//                            PercentageOfOutstanding == 0.
// PercentageOfOutstanding  — when >0, fee = outstanding × pct/100.
//                            Capped at CapAmount when set.
// CapAmount                — upper bound for percentage-shape fees.
//                            Zero/negative = no cap.
// GraceDays                — fee only eligible when
//                            now ≥ due + grace_days.
// Disabled                 — explicit corporate exemption switch
//                            (TC-LF-002). Wins over everything else.
type LateFeePolicy struct {
	FlatAmount              float64
	PercentageOfOutstanding float64
	CapAmount               float64
	GraceDays               int
	Disabled                bool
}

// DefaultLateFeePolicy returns the policy applied when neither the
// schema nor the legacy billing.policies row provides anything. Keeps
// behaviour predictable on a green-field install.
func DefaultLateFeePolicy() LateFeePolicy {
	return LateFeePolicy{
		FlatAmount: 25000,
		GraceDays:  3,
	}
}

// LateFeeEvalInput is the per-invoice projection the evaluator passes
// to Compute / IsEligible. Narrow on purpose — the domain function
// must not depend on the full InvoiceView.
type LateFeeEvalInput struct {
	InvoiceID         uuid.UUID
	DueDate           time.Time
	IsPaid            bool
	IsCancelled       bool
	OutstandingAmount float64
}

// IsEligible reports whether a late fee may be applied to the invoice
// at `now`. False when:
//
//   * the policy is disabled (corporate exemption);
//   * the invoice is paid or cancelled;
//   * we're still within the grace window;
//   * the outstanding balance is zero (or negative — refunded).
//
// Caller is also responsible for the per-invoice idempotency check
// against billing.late_fee_applications; this helper is purely about
// "should we ever charge?".
func (p LateFeePolicy) IsEligible(in LateFeeEvalInput, now time.Time) bool {
	if p.Disabled {
		return false
	}
	if in.IsPaid || in.IsCancelled {
		return false
	}
	if in.OutstandingAmount <= 0 {
		return false
	}
	grace := p.GraceDays
	if grace < 0 {
		grace = 0
	}
	cutoff := in.DueDate.AddDate(0, 0, grace)
	return !now.Before(cutoff)
}

// Compute returns the IDR amount to charge for the invoice. Returns 0
// when IsEligible == false so callers can skip the write without an
// extra branch. The percentage path takes precedence when set; the
// flat path applies otherwise. Caps clamp percentage results.
func (p LateFeePolicy) Compute(in LateFeeEvalInput, now time.Time) float64 {
	if !p.IsEligible(in, now) {
		return 0
	}
	if p.PercentageOfOutstanding > 0 {
		amount := in.OutstandingAmount * p.PercentageOfOutstanding / 100.0
		if p.CapAmount > 0 && amount > p.CapAmount {
			amount = p.CapAmount
		}
		return round2LateFee(amount)
	}
	if p.FlatAmount > 0 {
		return round2LateFee(p.FlatAmount)
	}
	return 0
}

// round2LateFee mirrors the invoice domain's rounding so we land on
// the same NUMERIC(18,2) cell finance expects.
func round2LateFee(f float64) float64 {
	if f >= 0 {
		return float64(int64(f*100+0.5)) / 100
	}
	return float64(int64(f*100-0.5)) / 100
}
