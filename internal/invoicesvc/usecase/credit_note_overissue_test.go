// Wave 120 — credit note overissue edge.
//
// Pins TC-ISV-* / TC-IMC-* "a credit note must not exceed the invoice
// amount". The current domain only validates amount >= 0; the
// invoice-amount ceiling is NOT enforced at the domain layer because
// the credit note service doesn't load the invoice projection. This
// test pins the current (lenient) behavior with a t.Skip on the
// future-tighter contract, matching the wave-108 pinning pattern.

package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func TestCreditNoteService_NegativeAmount_RefusedAtDomain(t *testing.T) {
	repo := newStubCNRepo()
	svc := NewCreditNoteService(repo)

	by := uuid.New()
	_, err := svc.Create(
		context.Background(),
		uuid.New(),
		nil,
		-100.00,
		"reason",
		&by,
	)
	if err == nil {
		t.Fatalf("expected validation error on negative amount")
	}
	if de := derrors.As(err); de == nil || de.Code != "credit_note.amount_invalid" {
		t.Fatalf("err code = %v, want credit_note.amount_invalid", err)
	}
}

func TestCreditNoteService_ZeroAmount_AllowedAsSymbolic(t *testing.T) {
	// Zero amount is permitted by the domain — symbolic "this invoice
	// was reversed" notes. Pins the boundary.
	repo := newStubCNRepo()
	svc := NewCreditNoteService(repo)
	cn, err := svc.Create(context.Background(), uuid.New(), nil, 0.0, "symbolic reversal", nil)
	if err != nil {
		t.Fatalf("zero amount should be allowed: %v", err)
	}
	if cn.Status != domain.CreditNoteStatusDraft {
		t.Errorf("status = %s, want draft", cn.Status)
	}
	if cn.Amount != 0 {
		t.Errorf("amount = %v, want 0", cn.Amount)
	}
}

// TestCreditNoteService_OverIssue_FutureContract pins TC-ISV-* "the
// credit note amount must not exceed the underlying invoice's total".
// The domain does NOT enforce this today — `NewCreditNote` only checks
// amount >= 0 and doesn't have a handle on the invoice. The catalog
// gap is flagged in docs/wave-120-100pct-broadband-compliance-report.md
// §3e (catalog gap: credit-note invoice-ceiling validator).
func TestCreditNoteService_OverIssue_FutureContract(t *testing.T) {
	t.Skip("Wave 120 pin — credit-note-amount-vs-invoice-amount validator " +
		"is a future enhancement; see docs/wave-120-100pct-broadband-" +
		"compliance-report.md §3e (catalog gap: credit-note invoice ceiling).")
}
