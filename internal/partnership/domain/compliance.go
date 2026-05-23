package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ComplianceStatus is the per-evaluation outcome.
type ComplianceStatus string

const (
	// ComplianceStatusRampSkipped — within agreement.ramp_months from
	// the reseller's first submission. The evaluator emits the row but
	// the achieved_pct doesn't count toward suspension/breach signals.
	ComplianceStatusRampSkipped ComplianceStatus = "ramp_skipped"

	// ComplianceStatusPassed — achieved >= compliance_threshold_pct.
	ComplianceStatusPassed ComplianceStatus = "passed"

	// ComplianceStatusBreached — achieved < compliance_threshold_pct.
	// Reason carries "achieved X% < threshold Y%". The usecase fires a
	// notifyx push to the reseller's contact on this status.
	ComplianceStatusBreached ComplianceStatus = "breached"
)

// ComplianceEvaluation is one row per (reseller, year, month) from the
// monthly compliance cron. The UNIQUE constraint on (reseller, year,
// month) keeps the cron idempotent — re-running the same period is a
// no-op rather than an INSERT.
type ComplianceEvaluation struct {
	ID                uuid.UUID
	ResellerAccountID uuid.UUID
	AgreementID       uuid.UUID
	PeriodYear        int
	PeriodMonth       int
	ThresholdPct      float64 // 0..1, copied from agreement
	AchievedPct       float64 // 0..1, computed
	Status            ComplianceStatus
	Reason            string
	EvaluatedAt       time.Time
}

// Evaluate computes a ComplianceEvaluation from a confirmed submission
// + the agreement in force at the submission's period_end. The caller
// (usecase) supplies monthsSinceFirstSubmission so the ramp-window
// check stays pure (the domain shouldn't query the DB).
//
// Rules:
//
//  1. If monthsSinceFirstSubmission < agreement.RampMonths, the row is
//     ramp_skipped with the achieved figure recorded for visibility
//     but no breach signal.
//
//  2. Otherwise, achieved = submission.NetRevenue / target_net_revenue
//     where target is pulled from agreement.TermsJSON["target_net_revenue"].
//     If the target is missing or 0, achieved defaults to 1.0 and the
//     status is passed (treat as "no target configured → no breach";
//     the operator should fix the agreement).
//
//  3. achieved >= ComplianceThresholdPct → passed
//     achieved <  ComplianceThresholdPct → breached
//     The reason on breach carries "achieved X.XX% < threshold Y.YY%".
//
// We don't take a *MonthlySubmission directly — the submission may be
// nil if the reseller didn't submit for this month. In that case the
// caller short-circuits with a separate "missing submission" path
// (handled by the usecase, not the domain).
func Evaluate(
	submission *MonthlySubmission,
	agreement *Agreement,
	monthsSinceFirstSubmission int,
	at time.Time,
) ComplianceEvaluation {
	out := ComplianceEvaluation{
		ID:                uuid.New(),
		ResellerAccountID: agreement.ResellerAccountID,
		AgreementID:       agreement.ID,
		PeriodYear:        submission.PeriodYear,
		PeriodMonth:       submission.PeriodMonth,
		ThresholdPct:      agreement.ComplianceThresholdPct,
		EvaluatedAt:       at,
	}

	// Ramp-window check first — these months don't count.
	if monthsSinceFirstSubmission < agreement.RampMonths {
		out.Status = ComplianceStatusRampSkipped
		out.Reason = fmt.Sprintf(
			"within ramp window: month %d of %d",
			monthsSinceFirstSubmission+1, agreement.RampMonths,
		)
		// Record achieved for the dashboard even though it doesn't
		// trigger a breach.
		if net := nonNilFloat(submission.NetRevenue); net != 0 {
			target := targetFromTerms(agreement.TermsJSON)
			if target > 0 {
				out.AchievedPct = net / target
			} else {
				out.AchievedPct = 1.0
			}
		}
		return out
	}

	// Achieved math.
	target := targetFromTerms(agreement.TermsJSON)
	net := nonNilFloat(submission.NetRevenue)
	var achieved float64
	if target > 0 {
		achieved = net / target
	} else {
		// Missing target → treat as "no target configured → passed".
		// The usecase logs a warning; we don't fail the row.
		achieved = 1.0
	}
	out.AchievedPct = achieved

	if achieved >= agreement.ComplianceThresholdPct {
		out.Status = ComplianceStatusPassed
		return out
	}

	out.Status = ComplianceStatusBreached
	out.Reason = fmt.Sprintf(
		"achieved %.2f%% < threshold %.2f%%",
		achieved*100, agreement.ComplianceThresholdPct*100,
	)
	return out
}

// targetFromTerms pulls target_net_revenue from the agreement terms
// JSON. Returns 0 if missing or non-numeric.
func targetFromTerms(terms map[string]any) float64 {
	if terms == nil {
		return 0
	}
	v, ok := terms["target_net_revenue"]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}

func nonNilFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
