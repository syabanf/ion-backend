package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — Edge #1: idempotent state-machine actions
//
// Calling Accept() twice on an already-Accepted row must return a typed
// Conflict — NOT a 200 / no-op. The audit doc explicitly classifies the
// double-action case under "invalid_state_transition" rather than
// silent idempotency: silent idempotency would mask retries during a
// state-flap window. The Wave 95 / Wave 100 domain methods all carry
// this contract; this test pins it so future "let's just no-op the
// second call" refactors get caught.
//
// We exercise the load-bearing accept paths:
//   - CustomerPO.Accept (validated -> accepted)
//   - IntercompanyPO.Accept (issued -> accepted)
//   - Settlement.Approve has its own idempotency-on-approved semantics
//     in partnership.usecase; the Wave 104 contract pin lives in
//     partnership/domain/settlement_sm_test.go (idempotent on approved)
// =====================================================================

// ----- fakes ---------------------------------------------------------

type stubCustomerPORepo struct {
	row *domain.CustomerPO
}

var _ port.CustomerPORepository = (*stubCustomerPORepo)(nil)

func (s *stubCustomerPORepo) Create(_ context.Context, _ *domain.CustomerPO) error { return nil }
func (s *stubCustomerPORepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.CustomerPO, error) {
	return s.row, nil
}
func (s *stubCustomerPORepo) List(_ context.Context, _ port.CustomerPOListFilter) ([]domain.CustomerPO, int, error) {
	return nil, 0, nil
}
func (s *stubCustomerPORepo) UpdateStatus(_ context.Context, po *domain.CustomerPO) error {
	s.row = po
	return nil
}

type stubIntercompanyPORepo struct {
	row   *domain.IntercompanyPO
	lines []domain.IntercompanyPOLine
}

var _ port.IntercompanyPORepository = (*stubIntercompanyPORepo)(nil)

func (s *stubIntercompanyPORepo) Create(_ context.Context, h *domain.IntercompanyPO, l []domain.IntercompanyPOLine) error {
	s.row = h
	s.lines = l
	return nil
}
func (s *stubIntercompanyPORepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.IntercompanyPO, error) {
	return s.row, nil
}
func (s *stubIntercompanyPORepo) List(_ context.Context, _ port.IntercompanyPOListFilter) ([]domain.IntercompanyPO, int, error) {
	return nil, 0, nil
}
func (s *stubIntercompanyPORepo) UpdateStatus(_ context.Context, po *domain.IntercompanyPO) error {
	s.row = po
	return nil
}
func (s *stubIntercompanyPORepo) FindLines(_ context.Context, _ uuid.UUID) ([]domain.IntercompanyPOLine, error) {
	return s.lines, nil
}

// ----- tests ---------------------------------------------------------

func TestIdempotency_CustomerPOAcceptDoubleConflicts(t *testing.T) {
	po, err := domain.NewCustomerPO(uuid.New(), uuid.New(), uuid.New(), "PO-1")
	if err != nil {
		t.Fatalf("NewCustomerPO: %v", err)
	}
	// pre-advance to accepted
	if err := po.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := po.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if po.Status != domain.CustomerPOStatusAccepted {
		t.Fatalf("setup status = %q, want accepted", po.Status)
	}
	repo := &stubCustomerPORepo{row: po}
	icRepo := &stubIntercompanyPORepo{}
	svc := (&Service{}).WithCustomerPOs(repo).WithIntercompanyPOs(icRepo, nil)
	// Re-invoking AcceptCustomerPO on an already-accepted row must
	// surface a typed Conflict.
	_, _, err = svc.AcceptCustomerPO(context.Background(), po.ID, nil)
	if err == nil {
		t.Fatal("second AcceptCustomerPO should fail with Conflict")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("kind = %v, want Conflict", de.Kind)
	}
	if de.Code != "customer_po.invalid_state_transition" {
		t.Errorf("code = %q, want customer_po.invalid_state_transition", de.Code)
	}
}

func TestIdempotency_IntercompanyPOAcceptDoubleConflicts(t *testing.T) {
	po, err := domain.NewIntercompanyPO(
		uuid.New(), uuid.New(),
		uuid.New(), uuid.New(),
		"ICPO-1",
	)
	if err != nil {
		t.Fatalf("NewIntercompanyPO: %v", err)
	}
	if err := po.Issue(); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	uid := uuid.New()
	if err := po.Accept(&uid); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	repo := &stubIntercompanyPORepo{row: po}
	svc := (&Service{}).WithIntercompanyPOs(repo, nil)
	_, err = svc.AcceptIntercompanyPO(context.Background(), po.ID, &uid)
	if err == nil {
		t.Fatal("second AcceptIntercompanyPO should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("kind = %v, want Conflict", de.Kind)
	}
	if de.Code != "intercompany_po.invalid_state_transition" {
		t.Errorf("code = %q, want intercompany_po.invalid_state_transition", de.Code)
	}
}

// Settlement.Approve is intentionally idempotent (see
// internal/partnership/domain/settlement.go:158). Document the contract
// here so a future audit doesn't mistake the partnership behavior for
// "we forgot to harden idempotency".
func TestIdempotency_SettlementApproveIsIdempotent_DocumentationOnly(t *testing.T) {
	// This is documentation; the actual settlement idempotency is
	// asserted in partnership/domain/settlement_sm_test.go via
	// "approved -> approved (idempotent)" row of the valid table.
	t.Log("settlement.Approve() is no-op on already-approved; see partnership/domain/settlement.go")
}
