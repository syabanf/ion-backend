package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewCreditNote_StartsAsDraft(t *testing.T) {
	cn, err := domainNewCN(t, 100, "refund")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cn.Status != CreditNoteStatusDraft {
		t.Errorf("expected draft, got %s", cn.Status)
	}
	if cn.CreditNo != "" {
		t.Errorf("credit_no should be empty until Issue, got %q", cn.CreditNo)
	}
}

func TestNewCreditNote_RejectsZeroInvoice(t *testing.T) {
	_, err := NewCreditNote(uuid.Nil, nil, 10, "x", nil)
	if err == nil {
		t.Fatal("expected error for zero invoice id")
	}
}

func TestNewCreditNote_RejectsNegativeAmount(t *testing.T) {
	_, err := NewCreditNote(uuid.New(), nil, -1, "x", nil)
	if err == nil {
		t.Fatal("expected error for negative amount")
	}
}

func TestCreditNote_IssueRequiresCreditNo(t *testing.T) {
	cn, _ := domainNewCN(t, 50, "r")
	if err := cn.Issue("", nil); err == nil {
		t.Fatal("expected error for empty credit_no")
	}
}

func TestCreditNote_IssueOnlyFromDraft(t *testing.T) {
	cn, _ := domainNewCN(t, 50, "r")
	if err := cn.Issue("CN-001", nil); err != nil {
		t.Fatal(err)
	}
	if cn.Status != CreditNoteStatusIssued {
		t.Errorf("expected issued, got %s", cn.Status)
	}
	// Re-issue must conflict.
	if err := cn.Issue("CN-002", nil); err == nil {
		t.Error("expected conflict on re-issue")
	}
}

func TestCreditNote_ApplyOnlyFromIssued(t *testing.T) {
	cn, _ := domainNewCN(t, 50, "r")
	// from draft → conflict
	if err := cn.Apply(time.Now()); err == nil {
		t.Fatal("expected conflict applying draft cn")
	}
	_ = cn.Issue("CN-001", nil)
	if err := cn.Apply(time.Now()); err != nil {
		t.Fatal(err)
	}
	if cn.Status != CreditNoteStatusApplied {
		t.Errorf("expected applied, got %s", cn.Status)
	}
	if cn.AppliedAt == nil {
		t.Error("applied_at should be set")
	}
	// Re-apply must conflict.
	if err := cn.Apply(time.Now()); err == nil {
		t.Error("expected conflict on re-apply")
	}
	if !cn.IsTerminal() {
		t.Error("applied is terminal")
	}
}

func TestCreditNote_VoidFromDraft(t *testing.T) {
	cn, _ := domainNewCN(t, 50, "r")
	by := uuid.New()
	if err := cn.Void(&by, "mistake"); err != nil {
		t.Fatal(err)
	}
	if cn.Status != CreditNoteStatusVoided {
		t.Errorf("expected voided, got %s", cn.Status)
	}
	if cn.VoidedAt == nil {
		t.Error("voided_at should be set")
	}
	if !cn.IsTerminal() {
		t.Error("voided is terminal")
	}
}

func TestCreditNote_VoidRequiresReason(t *testing.T) {
	cn, _ := domainNewCN(t, 50, "r")
	by := uuid.New()
	if err := cn.Void(&by, ""); err == nil {
		t.Fatal("expected validation error for empty void reason")
	}
}

func TestCreditNote_CannotVoidApplied(t *testing.T) {
	cn, _ := domainNewCN(t, 50, "r")
	_ = cn.Issue("CN-001", nil)
	_ = cn.Apply(time.Now())
	by := uuid.New()
	if err := cn.Void(&by, "oops"); err == nil {
		t.Error("expected conflict voiding applied cn")
	}
}

// Helper
func domainNewCN(t *testing.T, amount float64, reason string) (*CreditNote, error) {
	t.Helper()
	custID := uuid.New()
	return NewCreditNote(uuid.New(), &custID, amount, reason, nil)
}
