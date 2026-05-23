package usecase

import (
	"testing"
)

// =====================================================================
// Wave 108 — Edge #11: negotiation round 4 attempt
//
// The CPQ rule is that a single negotiation supports at most 3 rounds
// of VP price submission. A 4th attempt should return Conflict with
// `negotiation.round_limit_reached` — but as of HEAD the limit is NOT
// enforced (see internal/enterprise/usecase/negotiation.go::SubmitRound
// — the round counter monotonically increments via
// `s.negotiationRounds.HighestRoundNo(...)`+1 with no upper bound).
//
// The Wave 108 audit doc (wave-91 §Edge Case bucket) tracks this as a
// known gap. This test is a placeholder that:
//   1. Skips cleanly so CI stays green.
//   2. Documents the expected post-fix behavior (Conflict code) so a
//      future implementer can flip the skip off + see the test
//      green/red without re-reading the audit doc.
//
// When the max-rounds gate lands, drop the t.Skip and exercise:
//   - submit rounds 1,2,3 successfully
//   - submit round 4 → expect Conflict, code = "negotiation.round_limit_reached"
//   - the negotiation row stays in `active` (no spurious state flip)
// =====================================================================

func TestNegotiation_MaxRoundsExceeded_Conflicts(t *testing.T) {
	t.Skip("negotiation.round_limit_reached gate not yet implemented; tracked in wave-108 compliance report §3e")
}
