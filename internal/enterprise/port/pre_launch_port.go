package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// E7 — PO documents
// =====================================================================

type UploadPODocumentInput struct {
	OpportunityID uuid.UUID
	PONumber      string
	FileURL       string
	FileName      string
	FileSizeBytes int64
	ContentType   string
	IssuedByPIC   string
	Notes         string
	UploadedBy    *uuid.UUID
}

type PODocumentRepository interface {
	ListByOpportunity(ctx context.Context, opportunityID uuid.UUID) ([]domain.PODocument, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.PODocument, error)
	NextRevision(ctx context.Context, opportunityID uuid.UUID) (int, error)
	Create(ctx context.Context, d *domain.PODocument) error
}

// =====================================================================
// E8 — payment proofs
// =====================================================================

type UploadPaymentProofInput struct {
	InvoicePaymentID uuid.UUID
	FileURL          string
	FileName         string
	FileSizeBytes    int64
	ContentType      string
	Notes            string
	UploadedBy       *uuid.UUID
}

type PaymentProofRepository interface {
	ListByPayment(ctx context.Context, paymentID uuid.UUID) ([]domain.PaymentProof, error)
	Create(ctx context.Context, p *domain.PaymentProof) error
}

// =====================================================================
// E9 — EWO checklist items
// =====================================================================

type EWOChecklistRepository interface {
	ListByEWO(ctx context.Context, ewoID uuid.UUID) ([]domain.EWOChecklistItem, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.EWOChecklistItem, error)
	CreateBatch(ctx context.Context, items []domain.EWOChecklistItem) error
	Update(ctx context.Context, it *domain.EWOChecklistItem) error
}

// EWOChecklistTemplate — reusable seed-list for the checklist editor.
type EWOChecklistTemplate struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Description string
	Active      bool
	// Raw jsonb body — array of {seq_no,label,description}. Kept as
	// []byte so the storage layer doesn't have to round-trip through
	// domain — callers unmarshal as needed.
	ItemsJSON   []byte
	CreatedBy   *uuid.UUID
	CreatedAt   string
	UpdatedAt   string
}

type EWOChecklistTemplateRepository interface {
	List(ctx context.Context, activeOnly bool) ([]EWOChecklistTemplate, error)
	FindByID(ctx context.Context, id uuid.UUID) (*EWOChecklistTemplate, error)
	FindByCode(ctx context.Context, code string) (*EWOChecklistTemplate, error)
	// Upsert keys on `code`. Creates on first sight, replaces body on
	// repeat — admins iterate on the same template_code over time.
	Upsert(ctx context.Context, t EWOChecklistTemplate) (*EWOChecklistTemplate, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type ReplaceEWOChecklistInput struct {
	EWOID uuid.UUID
	Items []EWOChecklistItemInput
}

type EWOChecklistItemInput struct {
	SeqNo       int    `json:"seq_no"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type UpdateEWOChecklistItemInput struct {
	ItemID      uuid.UUID
	Status      string
	Notes       string
	CompletedBy *uuid.UUID
}

// =====================================================================
// E11 — projects / sites / services
// =====================================================================

type ProjectRepository interface {
	List(ctx context.Context, status string, opportunityID *uuid.UUID, limit, offset int) ([]domain.Project, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.Project, error)
	Create(ctx context.Context, p *domain.Project) error
	Update(ctx context.Context, p *domain.Project) error
}

type ProjectSiteRepository interface {
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectSite, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ProjectSite, error)
	Create(ctx context.Context, s *domain.ProjectSite) error
	Update(ctx context.Context, s *domain.ProjectSite) error
}

type EnterpriseServiceRepository interface {
	ListBySite(ctx context.Context, siteID uuid.UUID) ([]domain.EnterpriseService, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.EnterpriseService, error)
	Create(ctx context.Context, s *domain.EnterpriseService) error
	Update(ctx context.Context, s *domain.EnterpriseService) error
}

type CreateProjectInput struct {
	QuotationID            uuid.UUID
	ProjectManagerUserID   *uuid.UUID
	Notes                  string
}

type CreateProjectSiteInput struct {
	ProjectID uuid.UUID
	SiteCode  string
	SiteName  string
	Address   string
	Lat       *float64
	Lng       *float64
	PICName   string
	PICPhone  string
}

type CreateEnterpriseServiceInput struct {
	ProjectSiteID uuid.UUID
	BOQLineID     *uuid.UUID
	ServiceCode   string
	ServiceName   string
	Notes         string
}

// =====================================================================
// E12 — RFQ
// =====================================================================

type RFQRepository interface {
	List(ctx context.Context, status string, opportunityID *uuid.UUID, assignedTo *uuid.UUID, limit, offset int) ([]domain.RFQ, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.RFQ, error)
	Create(ctx context.Context, r *domain.RFQ) error
	Update(ctx context.Context, r *domain.RFQ) error
}

type CreateRFQInput struct {
	OpportunityID uuid.UUID
	Requirements  string
	Constraints   string
	DeadlineDays  int
	RequestedBy   *uuid.UUID
}

type AssignRFQInput struct {
	RFQID      uuid.UUID
	AssignedTo uuid.UUID
}

type FulfillRFQInput struct {
	RFQID         uuid.UUID
	FulfilledBOQID uuid.UUID
}

type CancelRFQInput struct {
	RFQID  uuid.UUID
	Reason string
}
