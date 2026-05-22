package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Inputs
// =====================================================================

type IssueInvoiceInput struct {
	QuotationID uuid.UUID
	DueDays     int        // defaults to 30
	IssuedBy    *uuid.UUID
	Notes       string
}

type RecordPaymentInput struct {
	InvoiceID  uuid.UUID
	Amount     float64
	Method     string
	Reference  string
	Notes      string
	PaidAt     string // optional ISO-date; defaults to now
	RecordedBy *uuid.UUID
}

type VoidInvoiceInput struct {
	InvoiceID  uuid.UUID
	Reason     string
	IfRevision *int
}

type InvoiceListFilter struct {
	Status        string
	OpportunityID *uuid.UUID
	QuotationID   *uuid.UUID
	Limit         int
	Offset        int
}

type EWOListFilter struct {
	Status        string
	OpportunityID *uuid.UUID
	QuotationID   *uuid.UUID
	AssignedTo    *uuid.UUID
	Limit         int
	Offset        int
}

type CreateEWOInput struct {
	QuotationID uuid.UUID
	Notes       string
}

type AssignEWOInput struct {
	EWOID      uuid.UUID
	AssignedTo uuid.UUID
}

type CancelEWOInput struct {
	EWOID  uuid.UUID
	Reason string
}

// =====================================================================
// UseCase
// =====================================================================

type FinanceUseCase interface {
	// Invoices
	ListInvoices(ctx context.Context, f InvoiceListFilter) ([]domain.Invoice, int, error)
	GetInvoice(ctx context.Context, id uuid.UUID) (*domain.Invoice, []domain.InvoicePayment, error)
	IssueInvoice(ctx context.Context, in IssueInvoiceInput) (*domain.Invoice, error)
	VoidInvoice(ctx context.Context, in VoidInvoiceInput) (*domain.Invoice, error)

	// Payments
	RecordPayment(ctx context.Context, in RecordPaymentInput) (*domain.InvoicePayment, *domain.Invoice, error)

	// EWOs
	ListEWOs(ctx context.Context, f EWOListFilter) ([]domain.EWO, int, error)
	GetEWO(ctx context.Context, id uuid.UUID) (*domain.EWO, error)
	CreateEWO(ctx context.Context, in CreateEWOInput) (*domain.EWO, error)
	AssignEWO(ctx context.Context, in AssignEWOInput) (*domain.EWO, error)
	StartEWO(ctx context.Context, id uuid.UUID) (*domain.EWO, error)
	CompleteEWO(ctx context.Context, id uuid.UUID) (*domain.EWO, error)
	CancelEWO(ctx context.Context, in CancelEWOInput) (*domain.EWO, error)

	// LinkEWOToFieldWO stamps the soft FK from an enterprise EWO to a
	// field-module work order. The field WO ID is not validated against
	// the enterprise schema — the field module lives in its own service.
	LinkEWOToFieldWO(ctx context.Context, ewoID, fieldWOID uuid.UUID) (*domain.EWO, error)

	// Hook: called from the quotation-accept path. Idempotent — re-firing
	// on a quotation that already produced an invoice + EWO returns the
	// existing rows without erroring. This is the inverse of
	// LockConfigOnApproval — a no-op when Phase 5 isn't wired.
	AutoCreateOnQuotationAccept(ctx context.Context, quotationID uuid.UUID) error
}

// =====================================================================
// Repositories
// =====================================================================

type InvoiceRepository interface {
	List(ctx context.Context, f InvoiceListFilter) ([]domain.Invoice, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Invoice, error)
	FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.Invoice, error)
	Create(ctx context.Context, inv *domain.Invoice) error
	Update(ctx context.Context, inv *domain.Invoice, ifRevision *int) error
}

type InvoicePaymentRepository interface {
	ListByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.InvoicePayment, error)
	Create(ctx context.Context, p *domain.InvoicePayment) error
}

type EWORepository interface {
	List(ctx context.Context, f EWOListFilter) ([]domain.EWO, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.EWO, error)
	FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.EWO, error)
	Create(ctx context.Context, e *domain.EWO) error
	Update(ctx context.Context, e *domain.EWO) error
	// LogCompletion is fired immediately after the EWO flips to
	// completed. It inserts a row into enterprise.ewo_completion_log
	// so the vendor-metrics derivation cron has a fresh row to chew
	// on. Idempotent — duplicate completes on the same ewo_id are
	// silently ignored (the derivation tick can still find the most
	// recent un-derived row).
	LogCompletion(ctx context.Context, ewoID uuid.UUID) error
}
