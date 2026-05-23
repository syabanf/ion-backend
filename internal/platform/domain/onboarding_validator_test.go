// Wave 116 — OnboardingValidator unit tests.
//
// Covers TC-SOB-001 (.exe reject), TC-SOB-002 (size cap), TC-SOB-004
// (required-doc missing), TC-SOB-012 (OCR confidence threshold).

package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOnboardingValidator_Kind(t *testing.T) {
	if (OnboardingValidator{}).Kind() != "onboarding" {
		t.Fatal("kind mismatch")
	}
}

func TestOnboardingValidator(t *testing.T) {
	v := OnboardingValidator{}

	tests := []struct {
		name      string
		body      string
		wantErrs  []string // substrings that must appear in errors
		wantWarns []string // substrings that must appear in warnings
		mustValid bool     // require errors to be empty
	}{
		{
			name: "minimal valid",
			body: `{
				"required_documents": [{"code":"ktp","allowed_formats":["jpeg","png","pdf"]}],
				"min_ocr_confidence": 0.80,
				"max_doc_size_mb": 10
			}`,
			mustValid: true,
		},
		{
			name:     "min_ocr_confidence missing",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"max_doc_size_mb":10}`,
			wantErrs: []string{"min_ocr_confidence_required"},
		},
		{
			name:     "min_ocr_confidence below 0.50",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":0.30,"max_doc_size_mb":10}`,
			wantErrs: []string{"min_ocr_confidence_out_of_range"},
		},
		{
			name:     "min_ocr_confidence above 1.0",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":1.5,"max_doc_size_mb":10}`,
			wantErrs: []string{"min_ocr_confidence_out_of_range"},
		},
		{
			name:     "max_doc_size_mb too large",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":0.80,"max_doc_size_mb":100}`,
			wantErrs: []string{"max_doc_size_mb_out_of_range"},
		},
		{
			name:     "max_doc_size_mb zero",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":0.80,"max_doc_size_mb":0}`,
			wantErrs: []string{"max_doc_size_mb_out_of_range"},
		},
		{
			name:     "no documents — TC-SOB-004",
			body:     `{"required_documents":[],"min_ocr_confidence":0.80,"max_doc_size_mb":10}`,
			wantErrs: []string{"required_documents_empty"},
		},
		{
			name:     "exe format disallowed — TC-SOB-001",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["exe","png"]}],"min_ocr_confidence":0.80,"max_doc_size_mb":10}`,
			wantErrs: []string{"format_disallowed"},
		},
		{
			name:     "doc code missing",
			body:     `{"required_documents":[{"code":"","allowed_formats":["png"]}],"min_ocr_confidence":0.80,"max_doc_size_mb":10}`,
			wantErrs: []string{"code_required"},
		},
		{
			name:     "auto_approve below ocr threshold",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":0.90,"max_doc_size_mb":10,"auto_approve_thresholds":{"enabled":true,"min_confidence":0.70}}`,
			wantErrs: []string{"auto_approve_threshold_too_low"},
		},
		{
			name:      "auto_approve enabled with weak OCR — warning",
			body:      `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":0.60,"max_doc_size_mb":10,"auto_approve_thresholds":{"enabled":true,"min_confidence":0.60}}`,
			wantWarns: []string{"auto_approve_enabled_without_strong_ocr"},
		},
		{
			name:     "timeline sla zero",
			body:     `{"required_documents":[{"code":"ktp","allowed_formats":["png"]}],"min_ocr_confidence":0.80,"max_doc_size_mb":10,"timeline_sla_hours":0}`,
			wantErrs: []string{"timeline_sla_hours_must_be_positive"},
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

// checkContains asserts each substring in `want` matches some entry in
// `got`. Empty `want` is a no-op.
func checkContains(t *testing.T, label string, got []string, want []string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			if strings.Contains(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s missing %q; got: %v", label, w, got)
		}
	}
}
