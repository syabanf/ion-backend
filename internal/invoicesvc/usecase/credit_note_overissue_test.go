// Wave 120 — credit note overissue edge.
//
// Pins TC-ISV-* / TC-IMC-* "a credit note must not exceed the invoice
// amount". The domain still only checks amount >= 0; the
// invoice-amount ceiling lives at the usecase layer (Wave 128B) and
// fires only when a cross-context InvoiceReader is wired into the
// service (production main.go always wires it; existing pre-Wave-128B
// unit tests that pass nil for the reader stay on the lenient path).

package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
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
// Closed in Wave 128B: NewCreditNoteServiceWithInvoices loads the
// invoice and the sum of existing issued+applied CNs, refusing any
// Create that would push the cumulative credited amount past the
// invoice Total.
func TestCreditNoteService_OverIssue_FutureContract(t *testing.T) {
	ctx := context.Background()
	repo := newStubCNRepo()
	reader := newStubReader()

	invID := uuid.New()
	custID := uuid.New()
	// Invoice total = 500.00. We'll first issue a 300.00 CN, then try
	// to create a 250.00 CN — total would be 550 (over the 500 ceiling
	// → reject).
	reader.byID[invID] = port.InvoiceProjection{
		ID:           invID,
		CustomerID:   custID,
		Total:        500.00,
		Status:       "issued",
		SourceModule: "billing",
	}
	svc := NewCreditNoteServiceWithInvoices(repo, reader)

	// First 300 — within the 500 ceiling, headroom is 500.
	cn1, err := svc.Create(ctx, invID, &custID, 300.00, "partial refund #1", nil)
	if err != nil {
		t.Fatalf("first Create within ceiling failed: %v", err)
	}
	// Advance it to 'issued' so it counts against the headroom.
	approver := uuid.New()
	if _, err := svc.Issue(ctx, cn1.ID, &approver); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Second 250 — would push total to 550 > 500 → reject.
	_, err = svc.Create(ctx, invID, &custID, 250.00, "partial refund #2", nil)
	if err == nil {
		t.Fatalf("second Create at 250 (over headroom 200) should have been rejected")
	}
	de := derrors.As(err)
	if de == nil || de.Code != "credit_note.exceeds_invoice" {
		t.Fatalf("err code = %v, want credit_note.exceeds_invoice", err)
	}
	if de.Kind != derrors.KindValidation {
		t.Errorf("err kind = %s, want validation", de.Kind)
	}

	// Exactly-at-headroom (200) must SUCCEED — the boundary is amount
	// > headroom, not >=.
	if _, err := svc.Create(ctx, invID, &custID, 200.00, "fill headroom", nil); err != nil {
		t.Errorf("Create at exact headroom should succeed, got: %v", err)
	}

	// Without a reader wired, the validator is a no-op (lenient path
	// preserved for pre-Wave-128B callers).
	lenientSvc := NewCreditNoteService(repo)
	if _, err := lenientSvc.Create(ctx, invID, &custID, 9999.99, "no reader = no gate", nil); err != nil {
		t.Errorf("lenient (no-reader) Create should succeed regardless of invoice total: %v", err)
	}
}
