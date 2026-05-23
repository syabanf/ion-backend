package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 108 — Edge #9: settlement on cancelled submission
//
// IssueSettlementForSubmission must refuse to fire when the parent
// MonthlySubmission is in any non-confirmed status. Cancelled is the
// trickiest case because the row still exists and an operator might
// expect "issue anyway" to silently succeed. The contract is the
// opposite: Conflict with `settlement.submission_not_confirmed` so
// the operator has to first re-open + re-confirm the submission, OR
// explicitly accept that no settlement should fire.
//
// Same shape as the IC-PO accept-before-issue test: pin the
// state-machine refusal at the service surface so a future
// "let's just be permissive" refactor gets caught.
// =====================================================================

// ----- fakes ---------------------------------------------------------

type stubSubmissionRepoForSettle struct {
	row *domain.MonthlySubmission
}

var _ port.MonthlySubmissionRepository = (*stubSubmissionRepoForSettle)(nil)

func (s *stubSubmissionRepoForSettle) Create(_ context.Context, _ *domain.MonthlySubmission) error {
	return nil
}

func (s *stubSubmissionRepoForSettle) FindByID(_ context.Context, _ uuid.UUID) (*domain.MonthlySubmission, error) {
	return s.row, nil
}

func (s *stubSubmissionRepoForSettle) FindByResellerPeriod(_ context.Context, _ uuid.UUID, _, _ int) (*domain.MonthlySubmission, error) {
	return s.row, nil
}

func (s *stubSubmissionRepoForSettle) List(_ context.Context, _ port.SubmissionListFilter) ([]domain.MonthlySubmission, int, error) {
	return nil, 0, nil
}

func (s *stubSubmissionRepoForSettle) UpdateFields(_ context.Context, _ *domain.MonthlySubmission) error {
	return nil
}

func (s *stubSubmissionRepoForSettle) UpdateStatus(_ context.Context, sub *domain.MonthlySubmission) error {
	s.row = sub
	return nil
}

func (s *stubSubmissionRepoForSettle) CountConfirmedBefore(_ context.Context, _ uuid.UUID, _, _ int) (int, error) {
	return 0, nil
}

type stubSettlementRepoForCancelled struct{}

var _ port.SettlementRepository = (*stubSettlementRepoForCancelled)(nil)

func (s *stubSettlementRepoForCancelled) Create(_ context.Context, _ *domain.Settlement) error {
	return nil
}

func (s *stubSettlementRepoForCancelled) FindByID(_ context.Context, _ uuid.UUID) (*domain.Settlement, error) {
	return nil, nil
}

func (s *stubSettlementRepoForCancelled) FindBySubmission(_ context.Context, _ uuid.UUID) (*domain.Settlement, error) {
	return nil, derrors.New(derrors.KindNotFound, "settlement.not_found", "no settlement for submission")
}

func (s *stubSettlementRepoForCancelled) List(_ context.Context, _ port.SettlementListFilter) ([]domain.Settlement, int, error) {
	return nil, 0, nil
}

func (s *stubSettlementRepoForCancelled) UpdateStatus(_ context.Context, _ *domain.Settlement) error {
	return nil
}

func (s *stubSettlementRepoForCancelled) UpdatePDF(_ context.Context, _ uuid.UUID, _, _ string) error {
	return nil
}

type stubAgreementRepoForCancelled struct{}

var _ port.AgreementRepository = (*stubAgreementRepoForCancelled)(nil)

func (s *stubAgreementRepoForCancelled) Create(_ context.Context, _ *domain.Agreement) error {
	return nil
}

func (s *stubAgreementRepoForCancelled) FindByID(_ context.Context, _ uuid.UUID) (*domain.Agreement, error) {
	return nil, derrors.New(derrors.KindNotFound, "agreement.not_found", "agreement not found")
}

func (s *stubAgreementRepoForCancelled) FindActive(_ context.Context, _ uuid.UUID, _ time.Time) (*domain.Agreement, error) {
	return nil, derrors.New(derrors.KindNotFound, "agreement.not_found", "no active agreement")
}

func (s *stubAgreementRepoForCancelled) List(_ context.Context, _ port.AgreementListFilter) ([]domain.Agreement, int, error) {
	return nil, 0, nil
}

func (s *stubAgreementRepoForCancelled) Update(_ context.Context, _ *domain.Agreement) error {
	return nil
}

func (s *stubAgreementRepoForCancelled) ListResellersWithActiveAgreement(_ context.Context, _ time.Time) ([]uuid.UUID, error) {
	return nil, nil
}

// ----- helpers -------------------------------------------------------

func newCancelledSubmissionRow(t *testing.T) *domain.MonthlySubmission {
	t.Helper()
	sub, err := domain.NewMonthlySubmission(uuid.New(), uuid.New(), 2026, 5)
	if err != nil {
		t.Fatalf("NewMonthlySubmission: %v", err)
	}
	if err := sub.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if sub.Status != domain.SubmissionStatusCancelled {
		t.Fatalf("setup status = %q, want cancelled", sub.Status)
	}
	return sub
}

// ----- tests ---------------------------------------------------------

func TestIssueSettlement_OnCancelledSubmission_Conflicts(t *testing.T) {
	sub := newCancelledSubmissionRow(t)
	subRepo := &stubSubmissionRepoForSettle{row: sub}
	stlRepo := &stubSettlementRepoForCancelled{}
	agRepo := &stubAgreementRepoForCancelled{}

	svc := NewSettlementService(stlRepo, subRepo, agRepo, nil, nil, nil, nil)

	_, err := svc.IssueSettlementForSubmission(context.Background(), sub.ID)
	if err == nil {
		t.Fatal("IssueSettlementForSubmission on cancelled submission must fail with Conflict")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("kind = %v, want Conflict", de.Kind)
	}
	if de.Code != "settlement.submission_not_confirmed" {
		t.Errorf("code = %q, want settlement.submission_not_confirmed", de.Code)
	}
}

// TestIssueSettlement_OnDraftSubmission_Conflicts — Draft is the most
// common case (operator clicked "issue" before the reseller submitted).
// Same code-path as cancelled; both must surface the same typed error.
func TestIssueSettlement_OnDraftSubmission_Conflicts(t *testing.T) {
	sub, err := domain.NewMonthlySubmission(uuid.New(), uuid.New(), 2026, 5)
	if err != nil {
		t.Fatalf("NewMonthlySubmission: %v", err)
	}
	if sub.Status != domain.SubmissionStatusDraft {
		t.Fatalf("setup status = %q, want draft", sub.Status)
	}
	subRepo := &stubSubmissionRepoForSettle{row: sub}
	stlRepo := &stubSettlementRepoForCancelled{}
	agRepo := &stubAgreementRepoForCancelled{}

	svc := NewSettlementService(stlRepo, subRepo, agRepo, nil, nil, nil, nil)

	_, err = svc.IssueSettlementForSubmission(context.Background(), sub.ID)
	if err == nil {
		t.Fatal("IssueSettlementForSubmission on draft must fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("kind = %v, want Conflict", de.Kind)
	}
}
