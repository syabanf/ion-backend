// Package usecase wires the reseller bounded context together.
//
// Service depends only on the port interfaces, never on the postgres
// adapters directly — that's what lets the bounded context move to
// its own service binary (cmd/reseller-svc) later without touching
// domain rules.
package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
)

// OnboardingService implements port.OnboardingUseCase.
//
// It only depends on the account repo today; the credit-ledger /
// wallet flows land in a later wave and will share this service
// rather than spinning up a new one.
type OnboardingService struct {
	accounts port.ResellerAccountRepository
}

// NewOnboardingService wires the onboarding flow.
func NewOnboardingService(accounts port.ResellerAccountRepository) *OnboardingService {
	return &OnboardingService{accounts: accounts}
}

var _ port.OnboardingUseCase = (*OnboardingService)(nil)

// OnboardReseller creates a new pending-KYC account. The HTTP handler
// pre-validates obvious shape errors (empty name, malformed email);
// this method enforces the domain-level invariants (name required,
// status defaults) and persists.
func (s *OnboardingService) OnboardReseller(ctx context.Context, in port.OnboardResellerInput) (*domain.ResellerAccount, error) {
	a, err := domain.NewResellerAccount(in.Name, in.NPWP, in.ContactEmail, in.ContactPhone)
	if err != nil {
		return nil, err
	}
	a.ParentSubsidiaryID = in.ParentSubsidiaryID
	if err := s.accounts.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// ApproveKYC flips pending_kyc → approved and records the approver.
// Re-approval from suspended is also allowed (see domain.ApproveKYC).
func (s *OnboardingService) ApproveKYC(ctx context.Context, id, approver uuid.UUID) (*domain.ResellerAccount, error) {
	a, err := s.accounts.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := a.ApproveKYC(approver); err != nil {
		return nil, err
	}
	if err := s.accounts.UpdateStatus(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Suspend pauses an approved account. Reason is mandatory at the
// domain level so the reseller portal can show a human-readable
// explanation.
func (s *OnboardingService) Suspend(ctx context.Context, id uuid.UUID, reason string) (*domain.ResellerAccount, error) {
	a, err := s.accounts.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := a.Suspend(reason); err != nil {
		return nil, err
	}
	if err := s.accounts.UpdateStatus(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Terminate is the irreversible exit. Idempotent on already-terminated.
func (s *OnboardingService) Terminate(ctx context.Context, id uuid.UUID) (*domain.ResellerAccount, error) {
	a, err := s.accounts.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := a.Terminate(); err != nil {
		return nil, err
	}
	if err := s.accounts.UpdateStatus(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *OnboardingService) ListAccounts(ctx context.Context, f port.ResellerListFilter) ([]domain.ResellerAccount, int, error) {
	return s.accounts.List(ctx, f)
}

func (s *OnboardingService) GetAccount(ctx context.Context, id uuid.UUID) (*domain.ResellerAccount, error) {
	return s.accounts.FindByID(ctx, id)
}
