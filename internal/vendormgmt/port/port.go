// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the vendor bounded context.
//
// Same hexagonal layout as reseller / partnership / enterprise: HTTP
// depends on a UseCase interface; the UseCase depends on repository
// interfaces; postgres adapters implement those repositories. The
// domain stays oblivious to both transport and storage so a future
// split into its own service (cmd/vendor-svc) doesn't touch domain
// rules.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
)

// =====================================================================
// Provider inputs
// =====================================================================

type CreateProviderInput struct {
	Name         string
	NPWP         string
	ContactEmail string
	ContactPhone string
	Capabilities []string
}

type UpdateProviderInput struct {
	ID           uuid.UUID
	Name         *string
	NPWP         *string
	ContactEmail *string
	ContactPhone *string
}

type ProviderListFilter struct {
	Status       string
	CapabilityIn []string // any-match across the supplied keys
	OnlyActive   bool
	Limit        int
	Offset       int
}

// =====================================================================
// Capability inputs
// =====================================================================

type AddCapabilityInput struct {
	ProviderID     uuid.UUID
	CapabilityKey  string
	CapabilityName string
	MaxCapacity    *int
}

// =====================================================================
// Submission inputs
// =====================================================================

type SubmitInputInput struct {
	OpportunityID uuid.UUID
	ProviderID    uuid.UUID
	BOQLineID     *uuid.UUID
	UnitCost      *float64
	Notes         string
	SubmittedBy   *uuid.UUID
}

type ReviewSubmissionInput struct {
	SubmissionID uuid.UUID
	Reviewer     uuid.UUID
	Reason       string // required on reject
}

type SubmissionListFilter struct {
	OpportunityID *uuid.UUID
	ProviderID    *uuid.UUID
	Status        string
	Limit         int
	Offset        int
}

// =====================================================================
// Metrics inputs
// =====================================================================

type RecordMetricInput struct {
	ProviderID           uuid.UUID
	MetricDate           time.Time
	JobsCompleted        int
	OnTimeCompletionPct  *float64
	AvgResponseHours     *float64
	TicketsResolved      int
	CustomerSatisfaction *float64
}

type TopRatedFilter struct {
	Capability string
	MinRating  float64
	MinJobs    int
	Limit      int
}

// =====================================================================
// UseCase contracts
// =====================================================================

type ProviderUseCase interface {
	Create(ctx context.Context, in CreateProviderInput) (*domain.Provider, error)
	Update(ctx context.Context, in UpdateProviderInput) (*domain.Provider, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Provider, []domain.ProviderCapability, error)
	List(ctx context.Context, f ProviderListFilter) ([]domain.Provider, int, error)

	// State machine
	CompleteKYC(ctx context.Context, id uuid.UUID) (*domain.Provider, error)
	Activate(ctx context.Context, id uuid.UUID) (*domain.Provider, error)
	Suspend(ctx context.Context, id uuid.UUID, reason string) (*domain.Provider, error)
	Reactivate(ctx context.Context, id uuid.UUID) (*domain.Provider, error)
	Blacklist(ctx context.Context, id uuid.UUID, reason string) (*domain.Provider, error)

	// Capabilities
	AddCapability(ctx context.Context, in AddCapabilityInput) (*domain.ProviderCapability, error)
	ListCapabilities(ctx context.Context, providerID uuid.UUID) ([]domain.ProviderCapability, error)
}

type SubmissionUseCase interface {
	Submit(ctx context.Context, in SubmitInputInput) (*domain.InputSubmission, error)
	Accept(ctx context.Context, in ReviewSubmissionInput) (*domain.InputSubmission, error)
	Reject(ctx context.Context, in ReviewSubmissionInput) (*domain.InputSubmission, error)
	Withdraw(ctx context.Context, submissionID uuid.UUID, submittedBy uuid.UUID) (*domain.InputSubmission, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.InputSubmission, error)
	List(ctx context.Context, f SubmissionListFilter) ([]domain.InputSubmission, int, error)
}

type MetricsUseCase interface {
	RecordDailyMetric(ctx context.Context, in RecordMetricInput) (*domain.DailyMetric, error)
	AverageScoreForProvider(ctx context.Context, providerID uuid.UUID, lookbackDays int) (float64, error)
	TopRatedProviders(ctx context.Context, f TopRatedFilter) ([]domain.Provider, error)
	ListDailyMetrics(ctx context.Context, providerID uuid.UUID, from, to time.Time) ([]domain.DailyMetric, error)
}

// =====================================================================
// Repository contracts (driven ports)
// =====================================================================

type ProviderRepository interface {
	Create(ctx context.Context, p *domain.Provider) error
	Update(ctx context.Context, p *domain.Provider) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Provider, error)
	List(ctx context.Context, f ProviderListFilter) ([]domain.Provider, int, error)
	// IncrementCompletedJob is the cross-context entry point used by the
	// enterprise IC-PO-accept hook. Adds 1 to total_completed_jobs +
	// `revenue` to total_revenue in a single atomic UPDATE so the math
	// is race-safe under concurrent IC-PO accepts.
	IncrementCompletedJob(ctx context.Context, providerID uuid.UUID, revenue float64) error
}

type ProviderCapabilityRepository interface {
	Create(ctx context.Context, c *domain.ProviderCapability) error
	ListByProvider(ctx context.Context, providerID uuid.UUID) ([]domain.ProviderCapability, error)
	ListProvidersByCapability(ctx context.Context, capabilityKey string) ([]uuid.UUID, error)
}

type SubmissionRepository interface {
	Create(ctx context.Context, s *domain.InputSubmission) error
	Update(ctx context.Context, s *domain.InputSubmission) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.InputSubmission, error)
	List(ctx context.Context, f SubmissionListFilter) ([]domain.InputSubmission, int, error)
}

type MetricsRepository interface {
	Upsert(ctx context.Context, m *domain.DailyMetric) error
	ListForProvider(ctx context.Context, providerID uuid.UUID, from, to time.Time) ([]domain.DailyMetric, error)
	AverageScore(ctx context.Context, providerID uuid.UUID, lookbackDays int) (float64, error)
	// TopRated returns providers ranked by rating × completion. The
	// query joins providers + provider_capabilities so a capability
	// filter is a SQL JOIN not a per-row scan.
	TopRated(ctx context.Context, f TopRatedFilter) ([]domain.Provider, error)
}
