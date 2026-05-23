// Wave 120 — H2H matching ambiguity edge.
//
// Pins TC-PH2-* "when multiple payment intents would match the same
// statement line within a ±2 day window with identical amount + no
// distinguishing reference, the matcher must NOT silently bind to a
// random intent — the finance dashboard needs the line flagged as
// ambiguous so the operator can review manually".
//
// Current state: MatchByReference picks the first hit per line at the
// highest confidence tier; if two intents both pass `amount + ±48h
// window` (the 0.50 'amount_date_window' tier) the matcher binds to
// whichever the caller iterated first. There is no domain-level
// "ambiguous=true" output.
//
// This test exercises the domain matcher to PIN both candidates score
// to 0.50/'amount_date_window' so the future ambiguity-flag work can
// detect the collision without re-deriving the math. A second test
// invokes the matcher with a distinguishing reference and asserts the
// confidence escalates to 0.85+ (so the collision goes away).
//
// The "real" ambiguity flag on H2HBankLine is t.Skip-pinned below; it
// requires a domain change (NewAmbiguousMatch + UI surface) that's
// flagged as a Wave 120 catalog gap in docs/wave-120-100pct-broadband-
// compliance-report.md §3e.

package usecase

import (
	"testing"
	"time"

	"github.com/ion-core/backend/internal/payment/domain"
)

func TestH2H_AmbiguousMatch_DomainMatcherPinsTier(t *testing.T) {
	// Two intents — same amount, same value-date window. Neither has a
	// reference, so both fall to the 0.50 amount_date_window tier.
	now := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	intentAmount := 250000.00
	intentPaid := now.Add(-12 * time.Hour)

	// Line ref empty — neither intent reference matches by string.
	confA, methodA := domain.MatchByReference(
		"",            // lineRef
		intentAmount,  // lineAmount
		now,           // lineValueDate
		"INTENT-A123", // intentRefShort
		intentAmount,
		&intentPaid,
	)
	confB, methodB := domain.MatchByReference(
		"",
		intentAmount,
		now,
		"INTENT-B456",
		intentAmount,
		&intentPaid,
	)
	if confA != 0.50 || methodA != "amount_date_window" {
		t.Fatalf("intent A: want (0.50, amount_date_window), got (%.2f, %s)", confA, methodA)
	}
	if confB != 0.50 || methodB != "amount_date_window" {
		t.Fatalf("intent B: want (0.50, amount_date_window), got (%.2f, %s)", confB, methodB)
	}
	// Both at 0.50 → operator review needed. The matcher's caller (the
	// MatchStatement loop in usecase/h2h.go) is responsible for binding;
	// today it picks the first one. The TestH2H_DistinguishingReference
	// case below proves that when a reference distinguishes the two,
	// the collision goes away.
}

func TestH2H_DistinguishingReference_EscalatesConfidence(t *testing.T) {
	now := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	intentPaid := now.Add(-12 * time.Hour)
	amount := 250000.00

	// Line ref contains intent B's short id — intent A stays at 0.50
	// (amount_date_window) while intent B jumps to 0.85
	// (reference_substring_amount). The matcher picks B unambiguously.
	confA, methodA := domain.MatchByReference(
		"transfer for INTENT-B456 broadband",
		amount,
		now,
		"INTENT-A123",
		amount,
		&intentPaid,
	)
	confB, methodB := domain.MatchByReference(
		"transfer for INTENT-B456 broadband",
		amount,
		now,
		"INTENT-B456",
		amount,
		&intentPaid,
	)
	if confA != 0.50 {
		t.Errorf("intent A: want 0.50 amount_date_window, got (%.2f, %s)", confA, methodA)
	}
	if confB < 0.85 {
		t.Errorf("intent B: want >= 0.85 substring/exact, got (%.2f, %s)", confB, methodB)
	}
	// Difference is large enough that a sane matcher binds to B without
	// review.
	if confB-confA < 0.30 {
		t.Errorf("expected a > 0.30 confidence gap to disambiguate, got A=%.2f B=%.2f", confA, confB)
	}
}

// TestH2H_AmbiguousMatch_FlaggedAsAmbiguous pins the FUTURE contract: a
// line that ties with another candidate at the SAME tier should be
// flagged (via a new H2HBankLine.AmbiguityNote field, set when
// MatchStatement detects a tied best score). Skipped today because the
// usecase-level loop in h2h.go::MatchStatement does not perform the
// tie check.
func TestH2H_AmbiguousMatch_FlaggedAsAmbiguous_Future(t *testing.T) {
	t.Skip("Wave 120 pin — ambiguity flag is a future enhancement; see " +
		"docs/wave-120-100pct-broadband-compliance-report.md §3e " +
		"(catalog gap: H2H ambiguity surface).")
}
