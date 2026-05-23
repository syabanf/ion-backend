// Wave 116 — SuspensionValidator unit tests.
//
// Covers TC-SSE-001..004 (warn/soft/hard monotonicity, RADIUS restore
// SLA, channel allow-list, partial-payment contradiction warning).

package domain

import (
	"encoding/json"
	"testing"
)

func TestSuspensionValidator_Kind(t *testing.T) {
	if (SuspensionValidator{}).Kind() != "suspension" {
		t.Fatal("kind mismatch")
	}
}

func TestSuspensionValidator(t *testing.T) {
	v := SuspensionValidator{}

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
				"warn_grace_days": 3,
				"soft_suspend_grace_days": 7,
				"hard_suspend_grace_days": 14,
				"radius_restore_window_minutes": 30,
				"notification_channels": ["whatsapp"]
			}`,
			mustValid: true,
		},
		{
			name: "non-monotonic warn=soft",
			body: `{
				"warn_grace_days": 7,"soft_suspend_grace_days":7,"hard_suspend_grace_days":14,
				"radius_restore_window_minutes":30,"notification_channels":["email"]
			}`,
			wantErrs: []string{"grace_days_non_monotonic"},
		},
		{
			name: "hard < soft — reversed",
			body: `{
				"warn_grace_days": 3,"soft_suspend_grace_days":14,"hard_suspend_grace_days":7,
				"radius_restore_window_minutes":30,"notification_channels":["email"]
			}`,
			wantErrs: []string{"grace_days_non_monotonic"},
		},
		{
			name: "grace day above 90",
			body: `{
				"warn_grace_days": 3,"soft_suspend_grace_days":7,"hard_suspend_grace_days":100,
				"radius_restore_window_minutes":30,"notification_channels":["email"]
			}`,
			wantErrs: []string{"hard_suspend_grace_days_out_of_range"},
		},
		{
			name: "restore window above 60min",
			body: `{
				"warn_grace_days": 3,"soft_suspend_grace_days":7,"hard_suspend_grace_days":14,
				"radius_restore_window_minutes":120,"notification_channels":["email"]
			}`,
			wantErrs: []string{"radius_restore_window_minutes_out_of_range"},
		},
		{
			name: "no channels",
			body: `{
				"warn_grace_days": 3,"soft_suspend_grace_days":7,"hard_suspend_grace_days":14,
				"radius_restore_window_minutes":30,"notification_channels":[]
			}`,
			wantErrs: []string{"notification_channels_empty"},
		},
		{
			name: "channel not allowed",
			body: `{
				"warn_grace_days": 3,"soft_suspend_grace_days":7,"hard_suspend_grace_days":14,
				"radius_restore_window_minutes":30,"notification_channels":["fax","email"]
			}`,
			wantErrs: []string{"notification_channel_disallowed"},
		},
		{
			name: "partial_advance + requires_full_settlement — warning",
			body: `{
				"warn_grace_days": 3,"soft_suspend_grace_days":7,"hard_suspend_grace_days":14,
				"radius_restore_window_minutes":30,"notification_channels":["whatsapp"],
				"partial_payment_advances":true,"restore_requires_full_settlement":true
			}`,
			wantWarns: []string{"partial_advance_contradicts_full_settlement"},
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
