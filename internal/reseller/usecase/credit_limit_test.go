package usecase

import (
	"testing"
)

// =====================================================================
// Wave 108 — Edge #5: Reseller credit-limit gate
//
// The CPQ rule: a wholesale order whose total exceeds the reseller's
// `credit_limit - balance` must be rejected with
// `reseller.credit_exhausted` at order submit (or create) time. The
// reseller domain already carries the columns (see
// internal/reseller/domain/reseller_account.go::CreditLimit + Balance,
// landed in Wave 94) but the wholesale usecase (CreateOrder / SubmitOrder)
// does NOT consult them — credit_limit is currently informational only.
//
// Wave 108 is a test-coverage + documentation pass; per the wave contract
// no business code may be modified. This test stays as a documented
// t.Skip pinning the future contract so that when the gate lands the
// implementer can flip the skip off without re-reading the audit doc.
//
// When the credit-limit gate lands, drop the t.Skip and exercise:
//   - reseller balance=0, credit_limit=1_000_000 → 1_500_000 order = reject
//     with Validation, code = "reseller.credit_exhausted", message
//     should embed both the limit + the current balance for the FE error
//   - reseller balance=200_000, credit_limit=1_000_000 → 800_000 order =
//     boundary (limit - balance == amount), should PASS
//   - reseller balance=200_000, credit_limit=1_000_000 → 800_001 order =
//     boundary +1, should reject
// =====================================================================

func TestWholesaleOrder_ExceedsCreditLimit_Rejected(t *testing.T) {
	t.Skip("reseller.credit_exhausted gate not yet implemented; tracked in wave-108 compliance report §3e")
}
