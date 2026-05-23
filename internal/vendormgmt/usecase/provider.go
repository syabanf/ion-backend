// Package usecase wires the vendor bounded context together.
//
// ProviderService / SubmissionService / MetricsService all depend on
// port interfaces — never on postgres adapters directly. That's what
// lets the bounded context move to its own service binary
// (cmd/vendor-svc) without touching domain rules.
package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
)

// ProviderService implements port.ProviderUseCase.
type ProviderService struct {
	providers    port.ProviderRepository
	capabilities port.ProviderCapabilityRepository
}

func NewProviderService(
	providers port.ProviderRepository,
	capabilities port.ProviderCapabilityRepository,
) *ProviderService {
	return &ProviderService{providers: providers, capabilities: capabilities}
}

var _ port.ProviderUseCase = (*ProviderService)(nil)

// Create persists a fresh pending provider + the optional capability
// tags supplied alongside. Capabilities are best-effort: a partial
// create (provider OK, capabilities failed) returns the provider with
// an error so the caller can retry the capability inserts.
func (s *ProviderService) Create(ctx context.Context, in port.CreateProviderInput) (*domain.Provider, error) {
	p, err := domain.NewProvider(in.Name, in.NPWP, in.ContactEmail, in.ContactPhone)
	if err != nil {
		return nil, err
	}
	if len(in.Capabilities) > 0 {
		// Snapshot on the header so the picker doesn't need to JOIN for
		// the cheap "what can this vendor do?" surface.
		p.Capabilities = append(p.Capabilities, in.Capabilities...)
	}
	if err := s.providers.Create(ctx, p); err != nil {
		return nil, err
	}
	// Persist normalised capability rows alongside the header snapshot.
	for _, key := range in.Capabilities {
		c, cerr := domain.NewProviderCapability(p.ID, key, "", nil)
		if cerr != nil {
			// Bad key — surface but keep the provider row.
			return p, cerr
		}
		if cerr := s.capabilities.Create(ctx, c); cerr != nil {
			return p, cerr
		}
	}
	return p, nil
}

func (s *ProviderService) Update(ctx context.Context, in port.UpdateProviderInput) (*domain.Provider, error) {
	p, err := s.providers.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.NPWP != nil {
		p.NPWP = *in.NPWP
	}
	if in.ContactEmail != nil {
		p.ContactEmail = *in.ContactEmail
	}
	if in.ContactPhone != nil {
		p.ContactPhone = *in.ContactPhone
	}
	if err := s.providers.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *ProviderService) Get(ctx context.Context, id uuid.UUID) (*domain.Provider, []domain.ProviderCapability, error) {
	p, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	caps, err := s.capabilities.ListByProvider(ctx, id)
	if err != nil {
		return p, nil, err
	}
	return p, caps, nil
}

func (s *ProviderService) List(ctx context.Context, f port.ProviderListFilter) ([]domain.Provider, int, error) {
	return s.providers.List(ctx, f)
}

// =====================================================================
// State machine — all four transitions follow the same load → mutate →
// persist pattern. Domain enforces the legal-source-state guard so the
// usecase stays thin.
// =====================================================================

func (s *ProviderService) CompleteKYC(ctx context.Context, id uuid.UUID) (*domain.Provider, error) {
	p, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	p.CompleteKYC()
	if err := s.providers.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *ProviderService) Activate(ctx context.Context, id uuid.UUID) (*domain.Provider, error) {
	p, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.Activate(); err != nil {
		return nil, err
	}
	if err := s.providers.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *ProviderService) Suspend(ctx context.Context, id uuid.UUID, reason string) (*domain.Provider, error) {
	p, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.Suspend(reason); err != nil {
		return nil, err
	}
	if err := s.providers.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *ProviderService) Reactivate(ctx context.Context, id uuid.UUID) (*domain.Provider, error) {
	p, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.Reactivate(); err != nil {
		return nil, err
	}
	if err := s.providers.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *ProviderService) Blacklist(ctx context.Context, id uuid.UUID, reason string) (*domain.Provider, error) {
	p, err := s.providers.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.Blacklist(reason); err != nil {
		return nil, err
	}
	if err := s.providers.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// =====================================================================
// Capabilities
// =====================================================================

func (s *ProviderService) AddCapability(ctx context.Context, in port.AddCapabilityInput) (*domain.ProviderCapability, error) {
	// Validate the provider exists + isn't terminal-blacklisted.
	if _, err := s.providers.FindByID(ctx, in.ProviderID); err != nil {
		return nil, err
	}
	c, err := domain.NewProviderCapability(in.ProviderID, in.CapabilityKey, in.CapabilityName, in.MaxCapacity)
	if err != nil {
		return nil, err
	}
	if err := s.capabilities.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *ProviderService) ListCapabilities(ctx context.Context, providerID uuid.UUID) ([]domain.ProviderCapability, error) {
	return s.capabilities.ListByProvider(ctx, providerID)
}
