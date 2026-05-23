// Wave 116 — BillingValidator unit tests.
//
// Covers TC-SBE-001..006 (mid-cycle edges, anniversary boundary, schema
// migrate). Cycle-day window + currency + prorate consistency are the
// load-bearing rules.

package domain

import (
	"encoding/json"
	"testing"
)

func TestBillingValidator_Kind(t *testing.T) {
	if (BillingValidator{}).Kind() != "billing" {
		t.Fatal("kind mismatch")
	}
}

func TestBillingValidator(t *testing.T) {
	v := BillingValidator{}

	tests := []struct {
		name      string
		body      string
		wantErrs  []string
		wantWarns []string
		mustValid bool
	}{
		{
			name: "minimal valid",
			body: `{
				"cycle_day": 1,
				"currency": "IDR",
				"prorate_policy": "full_period",
				"defer_policy": "first_invoice",
				"tax_mode": "exclusive",
				"tax_pct": 0.11
			}`,
			mustValid: true,
		},
		{
			name:     "cycle_day missing",
			body:     `{"currency":"IDR","prorate_policy":"full_period","defer_policy":"never","tax_mode":"none","tax_pct":0}`,
			wantErrs: []string{"cycle_day_required"},
		},
		{
			name:     "cycle_day above 28 — TC-SBE-004 boundary",
			body:     `{"cycle_day":31,"currency":"IDR","prorate_policy":"full_period","defer_policy":"never","tax_mode":"none","tax_pct":0}`,
			wantErrs: []string{"cycle_day_out_of_range"},
		},
		{
			name:     "currency unsupported",
			body:     `{"cycle_day":1,"currency":"XYZ","prorate_policy":"full_period","defer_policy":"never","tax_mode":"none","tax_pct":0}`,
			wantErrs: []string{"currency_unsupported"},
		},
		{
			name:     "prorate_policy invalid",
			body:     `{"cycle_day":1,"currency":"IDR","prorate_policy":"weekly","defer_policy":"never","tax_mode":"none","tax_pct":0}`,
			wantErrs: []string{"prorate_policy_invalid"},
		},
		{
			name:     "defer_policy invalid",
			body:     `{"cycle_day":1,"currency":"IDR","prorate_policy":"full_period","defer_policy":"sometimes","tax_mode":"none","tax_pct":0}`,
			wantErrs: []string{"defer_policy_invalid"},
		},
		{
			name:     "tax_pct above 0.50",
			body:     `{"cycle_day":1,"currency":"IDR","prorate_policy":"full_period","defer_policy":"never","tax_mode":"exclusive","tax_pct":0.75}`,
			wantErrs: []string{"tax_pct_out_of_range"},
		},
		{
			name:     "tax_pct negative",
			body:     `{"cycle_day":1,"currency":"IDR","prorate_policy":"full_period","defer_policy":"never","tax_mode":"exclusive","tax_pct":-0.05}`,
			wantErrs: []string{"tax_pct_out_of_range"},
		},
		{
			name:     "defer until_activated + prorate full_period — illegal",
			body:     `{"cycle_day":1,"currency":"IDR","prorate_policy":"full_period","defer_policy":"until_activated","tax_mode":"none","tax_pct":0}`,
			wantErrs: []string{"defer_policy_inconsistent_with_prorate"},
		},
		{
			name:      "tax_mode none with positive pct — warning",
			body:      `{"cycle_day":1,"currency":"IDR","prorate_policy":"full_period","defer_policy":"never","tax_mode":"none","tax_pct":0.11}`,
			wantWarns: []string{"tax_mode_none_but_pct_positive"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs, warns := v.Validate(json.RawMessage(tc.body))
			checkContains(t, "errors", errs, tc.wantErrs)
			checkContains(t, "warnings", warns, tc.wantWarns)
			if tc.mustValid && len(errs) != 0 {
				t.Errorf("expected no errors, got: %v", errs)
			}
		})
	}
}
