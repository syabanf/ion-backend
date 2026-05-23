package port

import (
	"context"
	"time"

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

	// Wave 96 — dual + scheduling filters.
	Side                    string // "x" | "y" | "" (any)
	ExecutingSubsidiaryID   *uuid.UUID
	IntercompanyPOID        *uuid.UUID
	PairedEWOID             *uuid.UUID
	AssignedTeamLeadUserID  *uuid.UUID
	AssignedTechnicianUserID *uuid.UUID
	ScheduledFrom           *time.Time
	ScheduledTo             *time.Time
}

// ScheduleUpdate is the per-call payload for EWORepository.UpdateSchedule.
// All four fields are required at call time; pass uuid.Nil / zero time
// only if the caller has already validated the absent values are
// intentional (the domain refuses zero-valued schedules).
type ScheduleUpdate struct {
	ScheduledStart time.Time
	ScheduledEnd   time.Time
	DurationDays   int
	TeamLead       uuid.UUID
	Technician     *uuid.UUID
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

	// Wave 96 — dual EWO + scheduling.
	//
	// FindBySide returns EWOs filtered by side (X | Y) along with the
	// usual list filter. Used by the auto-spawn path to locate an
	// existing EWO-X for a given (opportunity, BOQ) so the pair link
	// can be established when the EWO-Y is created.
	FindBySide(ctx context.Context, side domain.EWOSide, f EWOListFilter) ([]domain.EWO, error)
	// FindByPair returns the paired EWO (Y for an X, X for a Y) given
	// the input EWO id. Returns NotFound when no pair is set.
	FindByPair(ctx context.Context, ewoID uuid.UUID) (*domain.EWO, error)
	// UpdateSchedule writes only the scheduling columns. The Update
	// method is for status/assignment mutations and explicitly omits
	// the scheduling columns to keep the two surfaces independently
	// auditable.
	UpdateSchedule(ctx context.Context, ewoID uuid.UUID, sched ScheduleUpdate) error
	// LockSchedule flips schedule_locked → true without touching
	// status. Wave 96 — used when status transitions to in_progress
	// outside of the EWO.Start path (the dedicated MarkEWOInProgress
	// usecase).
	LockSchedule(ctx context.Context, ewoID uuid.UUID) error
	// UpdatePair persists the PairedEWOID set by EWO.LinkPair.
	UpdatePair(ctx context.Context, ewoID, pairedID uuid.UUID) error
	// FindOverlappingForTeamLead returns any EWO assigned to the same
	// team lead whose [scheduled_start, scheduled_end] window overlaps
	// the supplied range. excludeEWOID skips the current row (used by
	// Reschedule so an EWO doesn't conflict with itself).
	FindOverlappingForTeamLead(
		ctx context.Context,
		teamLeadID uuid.UUID,
		start, end time.Time,
		excludeEWOID *uuid.UUID,
	) ([]domain.EWO, error)
}

// EWOScheduleHistoryRepository persists the append-only reschedule
// audit trail. One row per Reschedule call; never updated or deleted.
type EWOScheduleHistoryRepository interface {
	Create(ctx context.Context, entry *domain.ScheduleHistoryEntry) error
	ListByEWO(ctx context.Context, ewoID uuid.UUID) ([]domain.ScheduleHistoryEntry, error)
}
