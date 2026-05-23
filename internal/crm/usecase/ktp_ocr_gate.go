// Wave 118 — KTP OCR confidence gate (TC-CRM-* / TC-SOB-005 regression edge).
//
// The audit (Wave 110) flagged TC-SOB-005 — schema-onboarding OCR confidence
// gate — as a gap: the existing ktp_ocr.go accepts any uploaded file and
// returns the OCR result, but does NOT gate lead acceptance on the
// confidence score. This file adds the pure-domain decision logic so the
// http handler (or any caller) can refuse acceptance when confidence is
// too low for an auto-pass.
//
// Threshold:
//   - confidence >= 0.80 → auto-accept (TC-CRM-OCR-001 path)
//   - 0 < confidence < 0.80 → reject (TC-CRM-OCR-002) — surface 422 to client
//   - confidence == 0 / missing → allow with warning (TC-CRM-OCR-003) —
//     OCR provider didn't return a score; treat as manual-review and let
//     the lead through. Future schema-driven config can tighten this.

package usecase

// KTPOCRGateThreshold is the auto-accept floor. Configurable by future
// schema policy (Schema Onboarding Deep, Wave 122); the literal lives
// here as the audit's stated default.
const KTPOCRGateThreshold = 0.80

// KTPOCRGateDecision is the gate's verdict.
type KTPOCRGateDecision string

const (
	KTPOCRDecisionAccept       KTPOCRGateDecision = "accept"
	KTPOCRDecisionReject       KTPOCRGateDecision = "reject"
	KTPOCRDecisionManualReview KTPOCRGateDecision = "manual_review"
)

// KTPOCRGateResult is what EvaluateKTPOCRGate returns. The caller maps
// rejections to a 422 with the AdvisoryMessage in the body.
type KTPOCRGateResult struct {
	Decision         KTPOCRGateDecision
	Confidence       float64
	AdvisoryMessage  string
	RecommendedAction string
}

// EvaluateKTPOCRGate applies the confidence threshold and returns the
// resulting decision. Pure function — no side effects.
//
// hasOCRResult must be false when the lead was created without a KTP
// upload (Mode B manual entry, per PRD §CRM §3.1). In that case the
// gate is a no-op (accept) — manual-entry leads don't go through OCR.
func EvaluateKTPOCRGate(hasOCRResult bool, confidence float64) KTPOCRGateResult {
	if !hasOCRResult {
		return KTPOCRGateResult{
			Decision:        KTPOCRDecisionAccept,
			Confidence:      0,
			AdvisoryMessage: "manual entry — no OCR evaluation",
		}
	}
	if confidence <= 0 {
		return KTPOCRGateResult{
			Decision:          KTPOCRDecisionManualReview,
			Confidence:        confidence,
			AdvisoryMessage:   "OCR returned no confidence score; lead accepted but flagged for manual review",
			RecommendedAction: "manual_kyc_review",
		}
	}
	if confidence < KTPOCRGateThreshold {
		return KTPOCRGateResult{
			Decision:          KTPOCRDecisionReject,
			Confidence:        confidence,
			AdvisoryMessage:   "KTP OCR confidence below threshold; re-upload a clearer photo",
			RecommendedAction: "reupload_ktp",
		}
	}
	return KTPOCRGateResult{
		Decision:   KTPOCRDecisionAccept,
		Confidence: confidence,
	}
}
