// Wave 118 — KTP OCR confidence gate tests (TC-CRM-* / TC-SOB-005).

package usecase

import (
	"testing"
)

func TestKTPOCRGate_HighConfidencePass(t *testing.T) {
	got := EvaluateKTPOCRGate(true, 0.95)
	if got.Decision != KTPOCRDecisionAccept {
		t.Fatalf("high confidence should accept, got %s", got.Decision)
	}
	if got.Confidence != 0.95 {
		t.Fatalf("confidence not preserved: %f", got.Confidence)
	}
}

func TestKTPOCRGate_LowConfidenceReject(t *testing.T) {
	got := EvaluateKTPOCRGate(true, 0.45)
	if got.Decision != KTPOCRDecisionReject {
		t.Fatalf("low confidence should reject, got %s", got.Decision)
	}
	if got.RecommendedAction != "reupload_ktp" {
		t.Fatalf("expected reupload action, got %s", got.RecommendedAction)
	}
}

func TestKTPOCRGate_MissingConfidenceAllowsWithWarning(t *testing.T) {
	got := EvaluateKTPOCRGate(true, 0)
	if got.Decision != KTPOCRDecisionManualReview {
		t.Fatalf("missing confidence should route to manual review, got %s", got.Decision)
	}
	if got.RecommendedAction != "manual_kyc_review" {
		t.Fatalf("expected manual_kyc_review action, got %s", got.RecommendedAction)
	}
}

func TestKTPOCRGate_ExactlyAtThreshold(t *testing.T) {
	// 0.80 == threshold → accept (>=).
	got := EvaluateKTPOCRGate(true, KTPOCRGateThreshold)
	if got.Decision != KTPOCRDecisionAccept {
		t.Fatalf("exactly at threshold should accept, got %s", got.Decision)
	}
}

func TestKTPOCRGate_ManualEntry_NoOCR(t *testing.T) {
	got := EvaluateKTPOCRGate(false, 0)
	if got.Decision != KTPOCRDecisionAccept {
		t.Fatalf("no OCR (manual entry) should accept, got %s", got.Decision)
	}
}
