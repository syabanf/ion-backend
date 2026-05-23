package usecase

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 107 — Payment proof + PPh23 + reminder unit tests.
// =====================================================================

type fakeInvoiceRepo struct {
	store map[uuid.UUID]*domain.Invoice
}

var _ port.InvoiceRepository = (*fakeInvoiceRepo)(nil)

func newFakeInvoiceRepo() *fakeInvoiceRepo {
	return &fakeInvoiceRepo{store: map[uuid.UUID]*domain.Invoice{}}
}

func (f *fakeInvoiceRepo) Create(_ context.Context, inv *domain.Invoice) error {
	cp := *inv
	f.store[inv.ID] = &cp
	return nil
}
func (f *fakeInvoiceRepo) Update(_ context.Context, inv *domain.Invoice, _ *int) error {
	cp := *inv
	f.store[inv.ID] = &cp
	return nil
}
func (f *fakeInvoiceRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Invoice, error) {
	if inv, ok := f.store[id]; ok {
		cp := *inv
		return &cp, nil
	}
	return nil, derrors.NotFound("invoice.not_found", "not found")
}
func (f *fakeInvoiceRepo) FindByQuotationID(_ context.Context, _ uuid.UUID) (*domain.Invoice, error) {
	return nil, derrors.NotFound("invoice.not_found", "not found")
}
func (f *fakeInvoiceRepo) List(_ context.Context, _ port.InvoiceListFilter) ([]domain.Invoice, int, error) {
	out := make([]domain.Invoice, 0, len(f.store))
	for _, v := range f.store {
		out = append(out, *v)
	}
	return out, len(out), nil
}

type fakeInvoicePaymentRepo struct {
	rows []domain.InvoicePayment
}

var _ port.InvoicePaymentRepository = (*fakeInvoicePaymentRepo)(nil)

func (f *fakeInvoicePaymentRepo) ListByInvoice(_ context.Context, _ uuid.UUID) ([]domain.InvoicePayment, error) {
	return f.rows, nil
}
func (f *fakeInvoicePaymentRepo) Create(_ context.Context, p *domain.InvoicePayment) error {
	f.rows = append(f.rows, *p)
	return nil
}

type fakePaymentProofRepo struct {
	rows []domain.PaymentProof
}

var _ port.PaymentProofRepository = (*fakePaymentProofRepo)(nil)

func (f *fakePaymentProofRepo) Create(_ context.Context, p *domain.PaymentProof) error {
	f.rows = append(f.rows, *p)
	return nil
}
func (f *fakePaymentProofRepo) ListByPayment(_ context.Context, _ uuid.UUID) ([]domain.PaymentProof, error) {
	return f.rows, nil
}

// newTestSvcForPolish wires the bare minimum of Service to exercise the
// finance-polish methods. PricebookRepository / OpportunityRepository
// are required by NewService but unused — pass nil.
func newTestSvcForPolish(invRepo *fakeInvoiceRepo, payRepo *fakeInvoicePaymentRepo, proofRepo *fakePaymentProofRepo) *Service {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return NewService(nil, nil, nil, log).
		WithFinance(invRepo, payRepo, nil).
		WithPaymentProofs(proofRepo)
}

func newSeededInvoice() *domain.Invoice {
	inv, _ := domain.NewInvoice(
		uuid.New(), uuid.New(), uuid.New(),
		"INV-001",
		1000.0, 900.0, 11.0,
		"IDR",
		time.Now().Add(7*24*time.Hour),
	)
	return inv
}

// TestSubmitPaymentProof_HappyPath — creates a placeholder payment row
// + a proof row, writes an audit entry.
func TestSubmitPaymentProof_HappyPath(t *testing.T) {
	inv := newSeededInvoice()
	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	payRepo := &fakeInvoicePaymentRepo{}
	proofRepo := &fakePaymentProofRepo{}
	svc := newTestSvcForPolish(invRepo, payRepo, proofRepo)

	user := uuid.New()
	proof, err := svc.SubmitPaymentProof(context.Background(), SubmitPaymentProofInput{
		InvoiceID:   inv.ID,
		FileURL:     "https://example.com/proof.pdf",
		FileHash:    "abc123",
		FileName:    "proof.pdf",
		ContentType: "application/pdf",
		FileSize:    1024,
		Notes:       "customer deposited",
		UploadedBy:  &user,
	})
	if err != nil {
		t.Fatalf("SubmitPaymentProof: %v", err)
	}
	if proof.FileURL != "https://example.com/proof.pdf" {
		t.Errorf("file_url = %q", proof.FileURL)
	}
	if len(payRepo.rows) != 1 {
		t.Fatalf("placeholder payment rows = %d, want 1", len(payRepo.rows))
	}
	if len(proofRepo.rows) != 1 {
		t.Fatalf("proof rows = %d, want 1", len(proofRepo.rows))
	}
}

// TestSubmitPaymentProof_VoidedInvoice — refuses to attach to a voided
// invoice.
func TestSubmitPaymentProof_VoidedInvoice(t *testing.T) {
	inv := newSeededInvoice()
	_ = inv.Void("operator cancelled")
	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	payRepo := &fakeInvoicePaymentRepo{}
	proofRepo := &fakePaymentProofRepo{}
	svc := newTestSvcForPolish(invRepo, payRepo, proofRepo)

	_, err := svc.SubmitPaymentProof(context.Background(), SubmitPaymentProofInput{
		InvoiceID: inv.ID,
		FileURL:   "https://example.com/proof.pdf",
	})
	if err == nil {
		t.Fatal("SubmitPaymentProof on voided should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "payment_proof.invoice_voided" {
		t.Errorf("err = %v, want payment_proof.invoice_voided", err)
	}
}

// TestVerifyPaymentProof_Approved — happy path returns nil.
func TestVerifyPaymentProof_Approved(t *testing.T) {
	inv := newSeededInvoice()
	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	payRepo := &fakeInvoicePaymentRepo{}
	proofRepo := &fakePaymentProofRepo{}
	svc := newTestSvcForPolish(invRepo, payRepo, proofRepo)

	if err := svc.VerifyPaymentProof(context.Background(), VerifyPaymentProofInput{
		ProofID:  uuid.New(),
		ByUserID: uuid.New(),
		Decision: "approved",
		Amount:   1000.0,
	}); err != nil {
		t.Fatalf("VerifyPaymentProof approved: %v", err)
	}
}

// TestVerifyPaymentProof_RejectedRequiresReason — bare reject errors out.
func TestVerifyPaymentProof_RejectedRequiresReason(t *testing.T) {
	inv := newSeededInvoice()
	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	svc := newTestSvcForPolish(invRepo, &fakeInvoicePaymentRepo{}, &fakePaymentProofRepo{})

	err := svc.VerifyPaymentProof(context.Background(), VerifyPaymentProofInput{
		ProofID:  uuid.New(),
		ByUserID: uuid.New(),
		Decision: "rejected",
	})
	if err == nil {
		t.Fatal("VerifyPaymentProof reject w/o reason should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "payment_proof.reject_reason_required" {
		t.Errorf("err = %v, want payment_proof.reject_reason_required", err)
	}
}

// TestVerifyPaymentProof_ApprovedRequiresAmount — approved with 0/neg
// amount errors.
func TestVerifyPaymentProof_ApprovedRequiresAmount(t *testing.T) {
	inv := newSeededInvoice()
	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	svc := newTestSvcForPolish(invRepo, &fakeInvoicePaymentRepo{}, &fakePaymentProofRepo{})

	err := svc.VerifyPaymentProof(context.Background(), VerifyPaymentProofInput{
		ProofID:  uuid.New(),
		ByUserID: uuid.New(),
		Decision: "approved",
		Amount:   0,
	})
	if err == nil {
		t.Fatal("VerifyPaymentProof approved w/ 0 amount should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "payment_proof.amount_required" {
		t.Errorf("err = %v, want payment_proof.amount_required", err)
	}
}

// TestVerifyPaymentProof_DecisionValidated — non-{approved,rejected}
// decision fails.
func TestVerifyPaymentProof_DecisionValidated(t *testing.T) {
	svc := newTestSvcForPolish(newFakeInvoiceRepo(), &fakeInvoicePaymentRepo{}, &fakePaymentProofRepo{})
	err := svc.VerifyPaymentProof(context.Background(), VerifyPaymentProofInput{
		ProofID:  uuid.New(),
		ByUserID: uuid.New(),
		Decision: "maybe",
	})
	if err == nil {
		t.Fatal("VerifyPaymentProof with bad decision should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "payment_proof.decision_invalid" {
		t.Errorf("err = %v, want payment_proof.decision_invalid", err)
	}
}

// TestSetInvoicePPh23_HappyPath — toggles + persists.
func TestSetInvoicePPh23_HappyPath(t *testing.T) {
	inv := newSeededInvoice()
	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	svc := newTestSvcForPolish(invRepo, &fakeInvoicePaymentRepo{}, &fakePaymentProofRepo{})

	out, err := svc.SetInvoicePPh23(context.Background(), inv.ID, true, 20.0)
	if err != nil {
		t.Fatalf("SetInvoicePPh23: %v", err)
	}
	if !out.IsPPh23Applicable || out.PPh23WithheldAmount != 20.0 {
		t.Errorf("invoice state mismatch: %+v", out)
	}
	if out.NetReceived() != out.TotalAmount-20.0 {
		t.Errorf("NetReceived = %v", out.NetReceived())
	}
}

// TestListInvoicesDueSoon_FiltersByDueWindow — only returns invoices
// due within the supplied window.
func TestListInvoicesDueSoon_FiltersByDueWindow(t *testing.T) {
	now := time.Now().UTC()
	inv1 := newSeededInvoice() // due in 7d (default)
	inv2 := newSeededInvoice()
	inv2.DueAt = now.Add(2 * 24 * time.Hour) // 2 days out
	inv3 := newSeededInvoice()
	inv3.DueAt = now.Add(10 * 24 * time.Hour) // far out

	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv1.ID] = inv1
	invRepo.store[inv2.ID] = inv2
	invRepo.store[inv3.ID] = inv3
	svc := newTestSvcForPolish(invRepo, &fakeInvoicePaymentRepo{}, &fakePaymentProofRepo{})

	out, err := svc.ListInvoicesDueSoon(context.Background(), 3)
	if err != nil {
		t.Fatalf("ListInvoicesDueSoon: %v", err)
	}
	// inv2 is 2 days out → should match. inv1 (7d) + inv3 (10d) outside.
	if len(out) != 1 || out[0].ID != inv2.ID {
		t.Errorf("matched ids = %+v, want [%s]", out, inv2.ID)
	}
}

// TestListInvoicesDueSoon_SkipsRecentlyReminded — invoices stamped
// within the dedupe window get filtered out.
func TestListInvoicesDueSoon_SkipsRecentlyReminded(t *testing.T) {
	now := time.Now().UTC()
	inv := newSeededInvoice()
	inv.DueAt = now.Add(2 * 24 * time.Hour) // matches the window
	// stamped 1 day ago (within due-3d window)
	t1 := now.Add(-24 * time.Hour)
	inv.ReminderSentAt = &t1

	invRepo := newFakeInvoiceRepo()
	invRepo.store[inv.ID] = inv
	svc := newTestSvcForPolish(invRepo, &fakeInvoicePaymentRepo{}, &fakePaymentProofRepo{})

	out, err := svc.ListInvoicesDueSoon(context.Background(), 3)
	if err != nil {
		t.Fatalf("ListInvoicesDueSoon: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected dedupe, got %+v", out)
	}
}
