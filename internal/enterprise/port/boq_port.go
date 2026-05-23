package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// SLA template inputs
// =====================================================================

type CreateSLATemplateInput struct {
	Key         string
	Name        string
	Description string
	Details     []byte // raw JSONB
}

type UpdateSLATemplateInput struct {
	ID          uuid.UUID
	Name        *string
	Description *string
	Details     []byte
	Active      *bool
}

// =====================================================================
// Approval template inputs
// =====================================================================

type ApprovalTemplateMemberInput struct {
	UserID  uuid.UUID
	StepNo  int
	RoleTag string
}

type CreateApprovalTemplateInput struct {
	Key         string
	Name        string
	Mode        string // 'sequential' | 'parallel'
	Description string
	Members     []ApprovalTemplateMemberInput
}

type UpdateApprovalTemplateInput struct {
	ID          uuid.UUID
	Name        *string
	Description *string
	Active      *bool
	// Members nil → leave as-is. Non-nil → replace the full member set.
	Members *[]ApprovalTemplateMemberInput
}

// =====================================================================
// BOQ inputs
// =====================================================================

type CreateBOQInput struct {
	OpportunityID uuid.UUID
	PricebookID   uuid.UUID
	Notes         string
	CreatedBy     *uuid.UUID
}

type UpdateBOQInput struct {
	ID         uuid.UUID
	Notes      *string
	IfRevision *int
}

type CreateBOQLineInput struct {
	BOQVersionID    uuid.UUID
	PricebookLineID uuid.UUID
	SLATemplateID   uuid.UUID
	Quantity        float64
	Notes           string
	SortOrder       int
}

type UpdateBOQLineInput struct {
	ID                        uuid.UUID
	Quantity                  *float64
	SellUnitPrice             *float64
	LineDiscountPct           *float64
	AssignedProviderCompanyID *uuid.UUID
	ProviderUserID            *uuid.UUID
	SLATemplateID             *uuid.UUID
	Notes                     *string
	SortOrder                 *int
}

// SetVendorCostInput — vendor-scoped endpoint payload. The handler
// pulls the actor user ID from JWT and asserts ownership.
type SetVendorCostInput struct {
	LineID         uuid.UUID
	VendorUnitCost float64
	ActorUserID    uuid.UUID
}

type SubmitBOQInput struct {
	BOQVersionID       uuid.UUID
	ApprovalTemplateID uuid.UUID
	IfRevision         *int
}

type ApprovalActionInput struct {
	InstanceID  uuid.UUID
	ActorUserID uuid.UUID
	ReasonCode  string // required on reject only
	Comment     string // required on reject only
}

type BOQListFilter struct {
	OpportunityID      *uuid.UUID
	ApprovalTemplateID *uuid.UUID
	Status             string
	Search             string
	Limit              int
	Offset             int
}

type ApprovalInstanceListFilter struct {
	// PendingForUserID: when set, returns only pending instances
	// where the assigned approver is this user (the "my queue" view).
	PendingForUserID *uuid.UUID
	BOQVersionID     *uuid.UUID
	Limit            int
	Offset           int
}

// =====================================================================
// Phase-3 UseCase additions — methods extend the same UseCase
// interface declared in port.go. We keep them in this separate file
// so the entrypoint stays scannable.
// =====================================================================

// BOQUseCase is the surface the HTTP layer depends on for Phase 3
// flows. The concrete Service still implements port.UseCase + this
// extra interface; we expose it as a separate name so consumers
// can depend on the narrowest contract.
type BOQUseCase interface {
	// SLA templates (admin)
	ListSLATemplates(ctx context.Context, activeOnly bool) ([]domain.SLATemplate, error)
	GetSLATemplate(ctx context.Context, id uuid.UUID) (*domain.SLATemplate, error)
	CreateSLATemplate(ctx context.Context, in CreateSLATemplateInput) (*domain.SLATemplate, error)
	UpdateSLATemplate(ctx context.Context, in UpdateSLATemplateInput) (*domain.SLATemplate, error)

	// Approval templates (admin)
	ListApprovalTemplates(ctx context.Context, activeOnly bool) ([]domain.ApprovalTemplate, error)
	GetApprovalTemplate(ctx context.Context, id uuid.UUID) (*domain.ApprovalTemplate, []domain.ApprovalTemplateMember, error)
	CreateApprovalTemplate(ctx context.Context, in CreateApprovalTemplateInput) (*domain.ApprovalTemplate, error)
	UpdateApprovalTemplate(ctx context.Context, in UpdateApprovalTemplateInput) (*domain.ApprovalTemplate, error)
	PublishApprovalTemplate(ctx context.Context, id uuid.UUID) (*domain.ApprovalTemplate, error)

	// BOQ — header CRUD
	ListBOQs(ctx context.Context, f BOQListFilter) ([]domain.BOQ, int, error)
	GetBOQ(ctx context.Context, id uuid.UUID) (*domain.BOQ, []domain.BOQLine, error)
	CreateBOQ(ctx context.Context, in CreateBOQInput) (*domain.BOQ, error)
	UpdateBOQ(ctx context.Context, in UpdateBOQInput) (*domain.BOQ, error)

	// BOQ — lines
	CreateBOQLine(ctx context.Context, in CreateBOQLineInput) (*domain.BOQLine, error)
	UpdateBOQLine(ctx context.Context, in UpdateBOQLineInput) (*domain.BOQLine, error)
	DeleteBOQLine(ctx context.Context, id uuid.UUID) error
	// SetVendorCost is the vendor-scoped endpoint — only the assigned
	// vendor user can call it, enforced by the usecase.
	SetVendorCost(ctx context.Context, in SetVendorCostInput) (*domain.BOQLine, error)

	// Submit / approve / reject
	SubmitBOQ(ctx context.Context, in SubmitBOQInput) (*domain.BOQ, []domain.ApprovalInstance, error)
	ApproveStep(ctx context.Context, in ApprovalActionInput) (*domain.ApprovalInstance, *domain.BOQ, error)
	RejectStep(ctx context.Context, in ApprovalActionInput) (*domain.ApprovalInstance, *domain.BOQ, error)
	// ReassignStep — Edge #3 / E3 pre-launch: hand a pending step off
	// to a different user without breaking the chain. Only allowed
	// while the step is still pending.
	ReassignStep(ctx context.Context, in ReassignStepInput) (*domain.ApprovalInstance, error)
	StartRevision(ctx context.Context, boqID uuid.UUID) (*domain.BOQ, error)

	// EditBOQAfterQuotation — Wave 106 TC-BQ-014. Auto-supersede an
	// approved BOQ post-quotation-issuance by spawning a fresh
	// revision_draft at version_no+1; the old row flips to superseded.
	EditBOQAfterQuotation(ctx context.Context, boqID uuid.UUID) (*domain.BOQ, error)

	// Approval instance queries
	ListApprovalInstances(ctx context.Context, f ApprovalInstanceListFilter) ([]domain.ApprovalInstance, error)
}

// ReassignStepInput swaps an approver on a still-pending step.
type ReassignStepInput struct {
	InstanceID uuid.UUID
	NewApprover uuid.UUID
	ActorUserID *uuid.UUID
	Reason      string
}

// =====================================================================
// Repository contracts (driven ports)
// =====================================================================

type SLATemplateRepository interface {
	List(ctx context.Context, activeOnly bool) ([]domain.SLATemplate, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SLATemplate, error)
	FindByKey(ctx context.Context, key string) (*domain.SLATemplate, error)
	Create(ctx context.Context, t *domain.SLATemplate) error
	Update(ctx context.Context, t *domain.SLATemplate) error
}

type ApprovalTemplateRepository interface {
	List(ctx context.Context, activeOnly bool) ([]domain.ApprovalTemplate, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ApprovalTemplate, error)
	FindByKey(ctx context.Context, key string) (*domain.ApprovalTemplate, error)
	Create(ctx context.Context, t *domain.ApprovalTemplate, members []domain.ApprovalTemplateMember) error
	Update(ctx context.Context, t *domain.ApprovalTemplate) error
	ListMembers(ctx context.Context, templateID uuid.UUID) ([]domain.ApprovalTemplateMember, error)
	ReplaceMembers(ctx context.Context, templateID uuid.UUID, members []domain.ApprovalTemplateMember) error
}

type BOQRepository interface {
	List(ctx context.Context, f BOQListFilter) ([]domain.BOQ, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BOQ, error)
	// FindHighestVersion returns the highest version_no row for a given
	// boq_number — used when computing the next version on resubmit.
	FindHighestVersion(ctx context.Context, boqNumber string) (*domain.BOQ, error)
	Create(ctx context.Context, b *domain.BOQ) error
	Update(ctx context.Context, b *domain.BOQ, ifRevision *int) error
	// SetSourceRFQID stamps the BOQ → RFQ backlink (E12 pre-launch).
	SetSourceRFQID(ctx context.Context, boqID, rfqID uuid.UUID) error
}

type BOQLineRepository interface {
	ListByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.BOQLine, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BOQLine, error)
	Create(ctx context.Context, l *domain.BOQLine) error
	Update(ctx context.Context, l *domain.BOQLine) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// VendorDueLine is the slim shape the SLA sweeper needs.
type VendorDueLine struct {
	LineID         uuid.UUID
	BOQVersionID   uuid.UUID
	ProviderUserID *uuid.UUID
	SKU            string
	VendorDueAt    time.Time
}

// InternalTransactionRepository persists the sub-company revenue
// ledger row generated when a BOQ flips to boq_approved.
type InternalTransactionRepository interface {
	CreateBatch(ctx context.Context, txs []domain.InternalTransaction) error
	ListByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.InternalTransaction, error)
	ListByVendor(ctx context.Context, vendorCompanyID uuid.UUID, from, to *string, limit, offset int) ([]domain.InternalTransaction, int, float64, float64, error)
}

// VendorSLASweeper — narrowly-typed cron helper. Implemented by the
// postgres BOQLineRepository; usecase tests can fake it without
// extending the full repo interface.
type VendorSLASweeper interface {
	ListVendorDueLines(ctx context.Context) ([]VendorDueLine, error)
	RecordVendorReminder(ctx context.Context, lineID uuid.UUID, bucket string) (bool, error)
}

type ApprovalInstanceRepository interface {
	ListByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.ApprovalInstance, error)
	List(ctx context.Context, f ApprovalInstanceListFilter) ([]domain.ApprovalInstance, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ApprovalInstance, error)
	CreateBatch(ctx context.Context, instances []domain.ApprovalInstance) error
	Update(ctx context.Context, a *domain.ApprovalInstance) error
}
