package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	"github.com/ion-core/backend/pkg/errors"
)

// CreditNoteService implements port.CreditNoteUseCase.
//
// State transitions go through the domain entity so the invariants
// (terminal applied/voided, draft-only issue) stay in one place. The
// repo is responsible for reading + writing — this service is glue.
type CreditNoteService struct {
	repo port.CreditNoteRepository
}

func NewCreditNoteService(repo port.CreditNoteRepository) *CreditNoteService {
	return &CreditNoteService{repo: repo}
}

var _ port.CreditNoteUseCase = (*CreditNoteService)(nil)

func (s *CreditNoteService) Create(
	ctx context.Context,
	invoiceID uuid.UUID,
	customerID *uuid.UUID,
	amount float64,
	reason string,
	createdBy *uuid.UUID,
) (*domain.CreditNote, error) {
	cn, err := domain.NewCreditNote(invoiceID, customerID, amount, reason, createdBy)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, cn); err != nil {
		return nil, err
	}
	return cn, nil
}

func (s *CreditNoteService) Issue(ctx context.Context, id uuid.UUID, approvedBy *uuid.UUID) (*domain.CreditNote, error) {
	cn, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	creditNo, err := s.repo.NextCreditNumber(ctx)
	if err != nil {
		return nil, err
	}
	if err := cn.Issue(creditNo, approvedBy); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, cn); err != nil {
		return nil, err
	}
	return cn, nil
}

func (s *CreditNoteService) Apply(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error) {
	cn, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := cn.Apply(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, cn); err != nil {
		return nil, err
	}
	return cn, nil
}

func (s *CreditNoteService) Void(ctx context.Context, id uuid.UUID, by *uuid.UUID, reason string) (*domain.CreditNote, error) {
	cn, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := cn.Void(by, reason); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, cn); err != nil {
		return nil, err
	}
	return cn, nil
}

func (s *CreditNoteService) List(ctx context.Context, f port.CreditNoteFilter) ([]domain.CreditNote, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return s.repo.List(ctx, f)
}

func (s *CreditNoteService) Get(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error) {
	return s.load(ctx, id)
}

func (s *CreditNoteService) load(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error) {
	if id == uuid.Nil {
		return nil, errors.Validation("credit_note.id_required", "credit note id is required")
	}
	cn, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if cn == nil {
		return nil, errors.NotFound("credit_note.not_found", "credit note not found")
	}
	return cn, nil
}
