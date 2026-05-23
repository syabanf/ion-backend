package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SubmissionService implements port.SubmissionUseCase.
type SubmissionService struct {
	submissions port.SubmissionRepository
	providers   port.ProviderRepository
}

func NewSubmissionService(
	submissions port.SubmissionRepository,
	providers port.ProviderRepository,
) *SubmissionService {
	return &SubmissionService{submissions: submissions, providers: providers}
}

var _ port.SubmissionUseCase = (*SubmissionService)(nil)

// Submit creates a fresh submitted row. Refuses non-operational providers
// (suspended / blacklisted / pending without KYC) so the review queue
// doesn't fill up with rows that can't possibly be accepted.
func (s *SubmissionService) Submit(ctx context.Context, in port.SubmitInputInput) (*domain.InputSubmission, error) {
	p, err := s.providers.FindByID(ctx, in.ProviderID)
	if err != nil {
		return nil, err
	}
	if !p.IsOperational() {
		return nil, derrors.Conflict(
			"submission.provider_not_operational",
			"provider must be active to file a cost submission",
		)
	}
	sub, err := domain.NewInputSubmission(
		in.OpportunityID, in.ProviderID, in.BOQLineID,
		in.UnitCost, in.Notes, in.SubmittedBy,
	)
	if err != nil {
		return nil, err
	}
	if err := s.submissions.Create(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubmissionService) Accept(ctx context.Context, in port.ReviewSubmissionInput) (*domain.InputSubmission, error) {
	sub, err := s.submissions.FindByID(ctx, in.SubmissionID)
	if err != nil {
		return nil, err
	}
	if err := sub.Accept(in.Reviewer); err != nil {
		return nil, err
	}
	if err := s.submissions.Update(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubmissionService) Reject(ctx context.Context, in port.ReviewSubmissionInput) (*domain.InputSubmission, error) {
	sub, err := s.submissions.FindByID(ctx, in.SubmissionID)
	if err != nil {
		return nil, err
	}
	if err := sub.Reject(in.Reviewer, in.Reason); err != nil {
		return nil, err
	}
	if err := s.submissions.Update(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// Withdraw is the submitter-only escape. We enforce the submitter check
// here because the domain doesn't know about authorization — only the
// usecase has the actor context.
func (s *SubmissionService) Withdraw(ctx context.Context, submissionID, submittedBy uuid.UUID) (*domain.InputSubmission, error) {
	sub, err := s.submissions.FindByID(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	// Submitter-or-nil guard: if the row carries a SubmittedBy and the
	// caller doesn't match, refuse. A nil SubmittedBy means the row was
	// created server-side without an actor context — permissive on that
	// edge so admin tooling can clean up.
	if sub.SubmittedBy != nil && *sub.SubmittedBy != submittedBy {
		return nil, derrors.Forbidden(
			"submission.withdraw_not_submitter",
			"only the original submitter can withdraw a submission",
		)
	}
	if err := sub.Withdraw(); err != nil {
		return nil, err
	}
	if err := s.submissions.Update(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubmissionService) Get(ctx context.Context, id uuid.UUID) (*domain.InputSubmission, error) {
	return s.submissions.FindByID(ctx, id)
}

func (s *SubmissionService) List(ctx context.Context, f port.SubmissionListFilter) ([]domain.InputSubmission, int, error) {
	return s.submissions.List(ctx, f)
}
