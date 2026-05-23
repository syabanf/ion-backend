// Package domain holds the partnership bounded context's entities and
// value objects.
//
// Rules (same as identity / crm / warehouse / enterprise / reseller domains):
//   - No imports of pkg/database, pkg/httpserver, or any framework.
//   - Constructors enforce invariants; callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map them
//     to the right status without inspecting strings.
//
// Wave 100 scope: Agreement, MonthlySubmission, Settlement, and
// ComplianceEvaluation. Together they cover Partnership Monthly
// Submission, Partnership Settlement, and Monthly Compliance Check TCs.
package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Agreement is the per-reseller revenue-share + compliance contract.
//
// terms_json carries the full agreement payload (target_net_revenue,
// payment_terms, support_level, etc.). The hot-path columns are hoisted
// out (RevsharePct, RampMonths, ComplianceThresholdPct) so the
// settlement formula + compliance evaluator can read them without
// re-parsing JSON on every row.
//
// effective_to is a *time.Time — nil means "open-ended". The lookup
// "active agreement at date X" matches when EffectiveFrom <= X AND
// (EffectiveTo == nil || EffectiveTo >= X).
type Agreement struct {
	ID                     uuid.UUID
	ResellerAccountID      uuid.UUID
	TermsJSON              map[string]any
	RevsharePct            float64 // 0..1, e.g. 0.30 = 30%
	RampMonths             int
	ComplianceThresholdPct float64 // 0..1, e.g. 0.80 = 80%
	EffectiveFrom          time.Time
	EffectiveTo            *time.Time
	SignedBy               *uuid.UUID
	SignedAt               *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// TermsSnapshot returns a stable, JSON-serializable copy of the
// agreement payload at this moment. The settlement layer calls this on
// confirm to freeze the contract terms onto the settlement row — later
// edits to the agreement never retroactively rewrite a closed
// settlement (TC-PS-005).
//
// We round-trip through json.Marshal/Unmarshal so the returned map is
// fully decoupled from the source map's underlying storage — mutating
// the result can't leak back into the live agreement.
func (a *Agreement) TermsSnapshot() map[string]any {
	if a.TermsJSON == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(a.TermsJSON)
	if err != nil {
		// terms_json originated from a DB JSONB column — re-marshal
		// failure shouldn't happen, but fall back to a shallow copy so
		// callers still get something usable.
		out := make(map[string]any, len(a.TermsJSON))
		for k, v := range a.TermsJSON {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// RevshareForRevenue applies the agreement's revshare formula to a
// net-revenue figure. Returns net * revshare_pct.
//
// Used by the settlement issuer in usecase/settlement.go. Kept on the
// domain so the formula has one canonical home — the cron and the
// PDF generator both go through this method, so any future change
// (tiered rates, currency rounding, etc.) lands in one place.
func (a *Agreement) RevshareForRevenue(netRevenue float64) float64 {
	if a == nil {
		return 0
	}
	return netRevenue * a.RevsharePct
}

// IsActiveAt reports whether this agreement is the one in force on
// date `at`. Used by repo + usecase to disambiguate when a reseller
// has multiple historical agreements.
func (a *Agreement) IsActiveAt(at time.Time) bool {
	if at.Before(a.EffectiveFrom) {
		return false
	}
	if a.EffectiveTo != nil && at.After(*a.EffectiveTo) {
		return false
	}
	return true
}
