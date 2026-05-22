package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
)

// TestCompleteEWO_LogsOnSuccess verifies the Wave 7 wiring: when an
// EWO transitions successfully from in_progress to completed, the
// usecase fires LogCompletion on the repo. The audit job (gap #7 in
// the post-Wave-6 audit) was that this hook was missing — keep it
// covered.
func TestCompleteEWO_LogsOnSuccess(t *testing.T) {
	id := uuid.New()
	repo := &fakeEWORepo{
		findByID: &domain.EWO{ID: id, Status: domain.EWOStatusInProgress},
	}
	s := (&Service{}).WithFinance(nil, nil, repo)

	out, err := s.CompleteEWO(context.Background(), id)
	if err != nil {
		t.Fatalf("CompleteEWO: %v", err)
	}
	if out.Status != domain.EWOStatusCompleted {
		t.Errorf("status = %q, want completed", out.Status)
	}
	if !repo.updateCalled {
		t.Error("Update was not called")
	}
	if !repo.logCompletionCalled {
		t.Error("LogCompletion was not called — Wave 7 hook is missing")
	}
	if repo.logCompletionEWOID != id {
		t.Errorf("LogCompletion got ewo %v, want %v", repo.logCompletionEWOID, id)
	}
}

// TestCompleteEWO_DomainErrorSkipsLog — the EWO domain refuses to
// flip from completed → completed. The usecase must surface that
// error AND must not have called LogCompletion (no successful
// transition happened).
func TestCompleteEWO_DomainErrorSkipsLog(t *testing.T) {
	id := uuid.New()
	repo := &fakeEWORepo{
		findByID: &domain.EWO{ID: id, Status: domain.EWOStatusCompleted},
	}
	s := (&Service{}).WithFinance(nil, nil, repo)

	if _, err := s.CompleteEWO(context.Background(), id); err == nil {
		t.Fatal("CompleteEWO on already-completed EWO should error")
	}
	if repo.updateCalled {
		t.Error("Update should not be called on a failed transition")
	}
	if repo.logCompletionCalled {
		t.Error("LogCompletion should not be called on a failed transition")
	}
}

// TestCompleteEWO_RepoUpdateErrorSkipsLog — if the DB Update fails,
// LogCompletion must not be called either; otherwise we'd log a
// completion that never persisted.
func TestCompleteEWO_RepoUpdateErrorSkipsLog(t *testing.T) {
	id := uuid.New()
	repo := &fakeEWORepo{
		findByID:  &domain.EWO{ID: id, Status: domain.EWOStatusInProgress},
		updateErr: errors.New("db down"),
	}
	s := (&Service{}).WithFinance(nil, nil, repo)

	if _, err := s.CompleteEWO(context.Background(), id); err == nil {
		t.Fatal("expected error when Update fails")
	}
	if repo.logCompletionCalled {
		t.Error("LogCompletion must not be called when Update errored")
	}
}

// TestCompleteEWO_LogErrorSwallowed — LogCompletion is best-effort;
// a failure there must NOT revert the successful completion. The
// usecase explicitly swallows the error (see finance.go).
func TestCompleteEWO_LogErrorSwallowed(t *testing.T) {
	id := uuid.New()
	repo := &fakeEWORepo{
		findByID:         &domain.EWO{ID: id, Status: domain.EWOStatusInProgress},
		logCompletionErr: errors.New("table missing"),
	}
	s := (&Service{}).WithFinance(nil, nil, repo)

	out, err := s.CompleteEWO(context.Background(), id)
	if err != nil {
		t.Fatalf("LogCompletion error must be swallowed; got: %v", err)
	}
	if out.Status != domain.EWOStatusCompleted {
		t.Errorf("status = %q, want completed", out.Status)
	}
}

// fakeEWORepo is a minimal in-memory EWORepository for the usecase
// tests. Only the methods CompleteEWO touches are non-trivial; the
// rest satisfy the interface with zero values so the test compiles.
type fakeEWORepo struct {
	findByID  *domain.EWO
	updateErr error

	updateCalled        bool
	logCompletionCalled bool
	logCompletionEWOID  uuid.UUID
	logCompletionErr    error
}

var _ port.EWORepository = (*fakeEWORepo)(nil)

func (f *fakeEWORepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.EWO, error) {
	return f.findByID, nil
}

func (f *fakeEWORepo) Update(_ context.Context, _ *domain.EWO) error {
	f.updateCalled = true
	return f.updateErr
}

func (f *fakeEWORepo) LogCompletion(_ context.Context, id uuid.UUID) error {
	f.logCompletionCalled = true
	f.logCompletionEWOID = id
	return f.logCompletionErr
}

// Stubs — not touched by the CompleteEWO tests.
func (f *fakeEWORepo) List(_ context.Context, _ port.EWOListFilter) ([]domain.EWO, int, error) {
	return nil, 0, nil
}
func (f *fakeEWORepo) FindByQuotationID(_ context.Context, _ uuid.UUID) (*domain.EWO, error) {
	return nil, nil
}
func (f *fakeEWORepo) Create(_ context.Context, _ *domain.EWO) error { return nil }
