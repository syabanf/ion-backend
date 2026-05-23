// Wave 116 — CommissionValidator unit tests.
//
// Largest validator → most cases. Covers TC-SCD-006 (clawback window),
// TC-SCD-009 (window boundary), TC-SCD-020..025 (split sums, ramp,
// flat-vs-pct), plus the Wave-114 percentage-form bridge.

package domain

import (
	"encoding/json"
	"testing"
)

func TestCommissionValidator_Kind(t *testing.T) {
	if (CommissionValidator{}).Kind() != "commission" {
		t.Fatal("kind mismatch")
	}
}

func TestCommissionValidator(t *testing.T) {
	v := CommissionValidator{}

	tests := []struct {
		name      string
		body      string
		wantErrs  []string
		wantWarns []string
		mustValid bool
	}{
		{
			name: "minimal valid percentage",
			body: `{
				"trigger_event": "on_paid",
				"recipient_role": "sales_person",
				"base_amount_basis": "first_invoice_pct",
				"percentage": 0.15,
				"clawback_days": 90,
				"ramp_months": 3
			}`,
			mustValid: true,
		},
		{
			name: "minimal valid flat",
			body: `{
				"trigger_event": "on_paid",
				"recipient_role": "sales_person",
				"base_amount_basis": "flat",
				"flat_amount": 50000,
				"clawback_days": 90,
				"ramp_months": 0
			}`,
			mustValid: true,
		},
		{
			name:     "trigger_event missing",
			body:     `{"recipient_role":"sales","base_amount_basis":"flat","flat_amount":100,"clawback_days":30,"ramp_months":0}`,
			wantErrs: []string{"trigger_event_required"},
		},
		{
			name:     "trigger_event invalid",
			body:     `{"trigger_event":"on_resignation","recipient_role":"sales","base_amount_basis":"flat","flat_amount":100,"clawback_days":30,"ramp_months":0}`,
			wantErrs: []string{"trigger_event_invalid"},
		},
		{
			name:     "recipient_role missing",
			body:     `{"trigger_event":"on_paid","base_amount_basis":"flat","flat_amount":100,"clawback_days":30,"ramp_months":0}`,
			wantErrs: []string{"recipient_role_required"},
		},
		{
			name:     "base_amount_basis invalid",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"weekly","clawback_days":30,"ramp_months":0}`,
			wantErrs: []string{"base_amount_basis_invalid"},
		},
		{
			name:     "percentage negative",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":-0.1,"clawback_days":30,"ramp_months":0}`,
			wantErrs: []string{"percentage_negative"},
		},
		{
			name:     "percentage > 100",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":120,"clawback_days":30,"ramp_months":0}`,
			wantErrs: []string{"percentage_too_large"},
		},
		{
			name:      "percentage in percent form 15 → warn",
			body:      `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":15,"clawback_days":30,"ramp_months":0}`,
			wantWarns: []string{"percentage_uses_percent_form"},
		},
		{
			name:     "clawback_days above 365",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,"clawback_days":400,"ramp_months":0}`,
			wantErrs: []string{"clawback_days_out_of_range"},
		},
		{
			name:     "clawback_days negative",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,"clawback_days":-1,"ramp_months":0}`,
			wantErrs: []string{"clawback_days_out_of_range"},
		},
		{
			name:     "clawback boundary 0 — valid",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,"clawback_days":0,"ramp_months":0}`,
			mustValid: true,
		},
		{
			name:     "ramp_months above 12",
			body:     `{"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,"clawback_days":30,"ramp_months":15}`,
			wantErrs: []string{"ramp_months_out_of_range"},
		},
		{
			name: "split_rules sum to 1.00",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,
				"clawback_days":30,"ramp_months":0,
				"split_rules":[{"role":"a","pct":0.5},{"role":"b","pct":0.3},{"role":"c","pct":0.2}]
			}`,
			mustValid: true,
		},
		{
			name: "split_rules sum to 0.95 — invalid",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,
				"clawback_days":30,"ramp_months":0,
				"split_rules":[{"role":"a","pct":0.5},{"role":"b","pct":0.3},{"role":"c","pct":0.15}]
			}`,
			wantErrs: []string{"split_rules_sum_invalid"},
		},
		{
			name: "split_rules role missing",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,
				"clawback_days":30,"ramp_months":0,
				"split_rules":[{"role":"","pct":1.0}]
			}`,
			wantErrs: []string{"role_required"},
		},
		{
			name: "flat basis but percentage set",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"flat",
				"flat_amount":1000,"percentage":0.1,"clawback_days":30,"ramp_months":0
			}`,
			wantErrs: []string{"percentage_forbidden_for_flat_basis"},
		},
		{
			name: "pct basis but flat_amount set",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct",
				"flat_amount":1000,"percentage":0.1,"clawback_days":30,"ramp_months":0
			}`,
			wantErrs: []string{"flat_amount_forbidden_for_pct_basis"},
		},
		{
			name: "pct basis missing percentage",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct",
				"clawback_days":30,"ramp_months":0
			}`,
			wantErrs: []string{"percentage_required_for_pct_basis"},
		},
		{
			name: "kyc + on_paid race — warning",
			body: `{
				"trigger_event":"on_paid","recipient_role":"x","base_amount_basis":"plan_pct","percentage":0.1,
				"clawback_days":30,"ramp_months":0,"requires_kyc":true
			}`,
			wantWarns: []string{"kyc_on_paid_race"},
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
