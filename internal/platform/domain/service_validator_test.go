// Wave 116 — ServiceValidator unit tests.
//
// Covers TC-SSD-001 (SLA tier resolution), speed sanity, monotonic
// degradation thresholds.

package domain

import (
	"encoding/json"
	"testing"
)

func TestServiceValidator_Kind(t *testing.T) {
	if (ServiceValidator{}).Kind() != "service" {
		t.Fatal("kind mismatch")
	}
}

func TestServiceValidator(t *testing.T) {
	v := ServiceValidator{}

	good := func() string {
		return `{
			"plan_name": "Home 50",
			"download_mbps": 50,
			"upload_mbps": 25,
			"sla_tier": "bronze",
			"qos_priority": 3,
			"radius_profile_template": "RES_50_25",
			"degradation_policy": {
				"warn_threshold_pct": 80,
				"soft_throttle_pct": 95,
				"hard_disconnect_pct": 100
			}
		}`
	}

	tests := []struct {
		name      string
		body      string
		wantErrs  []string
		wantWarns []string
		mustValid bool
	}{
		{name: "minimal valid", body: good(), mustValid: true},
		{
			name:     "plan_name missing",
			body:     `{"download_mbps":50,"upload_mbps":25,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"plan_name_required"},
		},
		{
			name:     "download out of range",
			body:     `{"plan_name":"P","download_mbps":99999,"upload_mbps":25,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"download_mbps_out_of_range"},
		},
		{
			name:     "upload zero",
			body:     `{"plan_name":"P","download_mbps":50,"upload_mbps":0,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"upload_mbps_must_be_positive"},
		},
		{
			name:     "upload > 2× download",
			body:     `{"plan_name":"P","download_mbps":10,"upload_mbps":50,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"upload_too_high_vs_download"},
		},
		{
			name:     "sla_tier invalid",
			body:     `{"plan_name":"P","download_mbps":50,"upload_mbps":25,"sla_tier":"diamond","qos_priority":3,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"sla_tier_invalid"},
		},
		{
			name:     "qos_priority out of range",
			body:     `{"plan_name":"P","download_mbps":50,"upload_mbps":25,"sla_tier":"bronze","qos_priority":9,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"qos_priority_out_of_range"},
		},
		{
			name:     "data_cap too small (typo guard)",
			body:     `{"plan_name":"P","download_mbps":50,"upload_mbps":25,"sla_tier":"bronze","qos_priority":3,"data_cap_gb":5,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"data_cap_gb_too_small"},
		},
		{
			name:     "degradation policy missing",
			body:     `{"plan_name":"P","download_mbps":50,"upload_mbps":25,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"X"}`,
			wantErrs: []string{"degradation_policy_required"},
		},
		{
			name:     "degradation non-monotonic",
			body:     `{"plan_name":"P","download_mbps":50,"upload_mbps":25,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"X","degradation_policy":{"warn_threshold_pct":95,"soft_throttle_pct":80,"hard_disconnect_pct":100}}`,
			wantErrs: []string{"degradation_policy_non_monotonic"},
		},
		{
			name:      "gold tier without bundle — warning",
			body:      `{"plan_name":"P","download_mbps":500,"upload_mbps":500,"sla_tier":"gold","qos_priority":5,"radius_profile_template":"X","bundled_services":[],"degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantWarns: []string{"high_tier_no_bundle"},
		},
		{
			name:      "missing radius template — warning",
			body:      `{"plan_name":"P","download_mbps":50,"upload_mbps":25,"sla_tier":"bronze","qos_priority":3,"radius_profile_template":"","degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			wantWarns: []string{"radius_profile_template_empty"},
		},
		{
			name: "platinum + bundle is fine",
			body: `{"plan_name":"P","download_mbps":1000,"upload_mbps":1000,"sla_tier":"platinum","qos_priority":7,"radius_profile_template":"PLAT","bundled_services":["vpn","static_ip"],"degradation_policy":{"warn_threshold_pct":80,"soft_throttle_pct":95,"hard_disconnect_pct":100}}`,
			mustValid: true,
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
