// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the enterprise bounded context.
//
// Same hexagonal pattern as identity / crm / warehouse: HTTP handlers
// depend on UseCase; UseCase depends on repository interfaces;
// postgres adapters implement the repository interfaces. This isolates
// the domain from both transport and storage so the bounded context
// can be extracted into its own service (cmd/enterprise-svc) without
// touching the domain.
package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Pricebook inputs
// =====================================================================

type CreatePricebookInput struct {
	Code             string
	Name             string
	Currency         string
	EffectiveFrom    string // ISO date (YYYY-MM-DD) — HTTP-friendly
	EffectiveTo      string // ISO date or empty for open-ended
	HoldingCompanyID string
	Notes            string
	CreatedBy        *uuid.UUID
}

type UpdatePricebookInput struct {
	ID            uuid.UUID
	Name          *string
	EffectiveFrom *string
	EffectiveTo  *string
	Notes        *string
}

type PricebookListFilter struct {
	// Status filters: empty = all. Common pickers:
	//   "published" → live catalog only
	//   "draft"     → admin's editing queue
	Status           string
	HoldingCompanyID string
	Code             string // exact match
	Limit            int
	Offset           int
}

type CreatePricebookLineInput struct {
	PricebookID               uuid.UUID
	SKU                       string
	Name                      string
	Category                  string
	Description               string
	Unit                      string
	BasePrice                 float64
	DefaultMarginPct          float64
	MinMarginPct              float64
	MaxDiscountPct            float64
	AllowedProviderCompanyIDs []uuid.UUID
	OwnerRole                 string
	SortOrder                 int
}

type UpdatePricebookLineInput struct {
	ID                        uuid.UUID
	Name                      *string
	Category                  *string
	Description               *string
	Unit                      *string
	BasePrice                 *float64
	DefaultMarginPct          *float64
	MinMarginPct              *float64
	MaxDiscountPct            *float64
	AllowedProviderCompanyIDs *[]uuid.UUID
	OwnerRole                 *string
	SortOrder                 *int
	Active                    *bool
}

// =====================================================================
// Opportunity inputs
// =====================================================================

type CreateOpportunityInput struct {
	AccountName        string
	AccountIndustry    string
	AccountSize        string
	PICName            string
	PICTitle           string
	PICPhone           string
	PICEmail           string
	OwnerUserID        *uuid.UUID
	BranchID           *uuid.UUID
	EstimatedValue     float64
	Currency           string
	ExpectedCloseAt    *string // ISO date
	Source             string
	ReferrerCustomerID *uuid.UUID
	CustomerID         *uuid.UUID
	Notes              string
}

type UpdateOpportunityInput struct {
	ID              uuid.UUID
	AccountName     *string
	AccountIndustry *string
	AccountSize     *string
	PICName         *string
	PICTitle        *string
	PICPhone        *string
	PICEmail        *string
	OwnerUserID     *uuid.UUID
	BranchID        *uuid.UUID
	EstimatedValue  *float64
	ExpectedCloseAt *string
	Notes           *string
	// Concurrency: pass the row's last-known revision to detect stale
	// updates (CPQ TC-CONC-005 → HTTP 409 stale_version).
	IfRevision *int
}

// AdvanceStageInput is the input for the stage-transition endpoint.
// Used for warm/hot/won — Lost has its own input (reason mandatory).
type AdvanceStageInput struct {
	ID         uuid.UUID
	TargetStage string // 'warm' | 'hot' | 'won'
	// Won-only field — opportunity must have a PO reference before Won.
	POReference string
	IfRevision  *int
}

type MarkLostInput struct {
	ID         uuid.UUID
	ReasonCode string
	Reason     string
	IfRevision *int
}

type CompletePreBOQInput struct {
	ID         uuid.UUID
	Snapshot   []byte // raw JSON bytes
	IfRevision *int
}

type PinPricebookInput struct {
	ID          uuid.UUID
	PricebookID uuid.UUID
	IfRevision  *int
}

type OpportunityListFilter struct {
	Stage              string // 'cold' | 'warm' | 'hot' | 'won' | 'lost' | ''
	OwnerUserID        *uuid.UUID
	BranchID           *uuid.UUID
	Search             string // matches account_name, opportunity_number
	IncludeArchivedLost bool   // by default Lost is hidden from the active pipeline
	Limit              int
	Offset             int
}

// =====================================================================
// UseCase — what the HTTP layer depends on
// =====================================================================

type UseCase interface {
	// Pricebooks
	ListPricebooks(ctx context.Context, f PricebookListFilter) ([]domain.Pricebook, int, error)
	GetPricebook(ctx context.Context, id uuid.UUID) (*domain.Pricebook, error)
	CreatePricebook(ctx context.Context, in CreatePricebookInput) (*domain.Pricebook, error)
	UpdatePricebook(ctx context.Context, in UpdatePricebookInput) (*domain.Pricebook, error)
	PublishPricebook(ctx context.Context, id uuid.UUID) (*domain.Pricebook, error)

	// Pricebook lines
	ListPricebookLines(ctx context.Context, pricebookID uuid.UUID) ([]domain.PricebookLine, error)
	CreatePricebookLine(ctx context.Context, in CreatePricebookLineInput) (*domain.PricebookLine, error)
	UpdatePricebookLine(ctx context.Context, in UpdatePricebookLineInput) (*domain.PricebookLine, error)
	DeletePricebookLine(ctx context.Context, id uuid.UUID) error

	// Opportunities — lifecycle
	ListOpportunities(ctx context.Context, f OpportunityListFilter) ([]domain.Opportunity, int, error)
	GetOpportunity(ctx context.Context, id uuid.UUID) (*domain.Opportunity, error)
	CreateOpportunity(ctx context.Context, in CreateOpportunityInput) (*domain.Opportunity, error)
	UpdateOpportunity(ctx context.Context, in UpdateOpportunityInput) (*domain.Opportunity, error)
	AdvanceStage(ctx context.Context, in AdvanceStageInput) (*domain.Opportunity, error)
	MarkLost(ctx context.Context, in MarkLostInput) (*domain.Opportunity, error)
	CompletePreBOQ(ctx context.Context, in CompletePreBOQInput) (*domain.Opportunity, error)
	PinPricebook(ctx context.Context, in PinPricebookInput) (*domain.Opportunity, error)

	// Auto-Lost scheduler — invoked by the cron job. Returns the IDs
	// that were flipped. Idempotent: calling twice has no extra effect.
	RunAutoLostSweep(ctx context.Context) (flipped []uuid.UUID, err error)
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================

type PricebookRepository interface {
	List(ctx context.Context, f PricebookListFilter) ([]domain.Pricebook, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Pricebook, error)
	// FindOverlapping returns existing pricebooks with the same `code`
	// whose effective window overlaps the candidate's. Used by the
	// pre-insert overlap check.
	FindOverlapping(ctx context.Context, candidate *domain.Pricebook) ([]domain.Pricebook, error)
	Create(ctx context.Context, p *domain.Pricebook) error
	Update(ctx context.Context, p *domain.Pricebook) error
}

type PricebookLineRepository interface {
	ListByPricebook(ctx context.Context, pricebookID uuid.UUID) ([]domain.PricebookLine, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.PricebookLine, error)
	Create(ctx context.Context, line *domain.PricebookLine) error
	Update(ctx context.Context, line *domain.PricebookLine) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type OpportunityRepository interface {
	List(ctx context.Context, f OpportunityListFilter) ([]domain.Opportunity, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Opportunity, error)
	Create(ctx context.Context, o *domain.Opportunity) error
	// Update uses optimistic concurrency control via Revision. Pass
	// `ifRevision = nil` to skip the check (admin overrides), `nil`
	// returns are treated as "no-op fine" by the usecase.
	Update(ctx context.Context, o *domain.Opportunity, ifRevision *int) error
	// FindExpiredAutoLostCandidates returns opportunities still in
	// non-terminal stages whose last_activity_at is older than the
	// stage's auto-Lost window. Used by RunAutoLostSweep.
	FindExpiredAutoLostCandidates(ctx context.Context) ([]domain.Opportunity, error)
}
