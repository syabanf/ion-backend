// Package port defines the contracts between the invoicesvc usecase
// layer and the world outside it.
//
// Three flavours of port live here:
//
//   - 4 driven repos (snapshot, credit_note, bulk_job, bulk_item) —
//     persistence boundary against `invoicesvc.*`.
//   - InvoiceReader cross-context port — SQL-only adapter reading
//     `billing.invoices` (and `enterprise.invoices` for unified
//     dashboards). Returns InvoiceProjection, NOT a full domain
//     object, so invoicesvc stays free of cross-context Go imports.
//   - InvoiceGenerator cross-context port — called by the bulk runner
//     to issue invoices via the upstream billing module. The default
//     wiring stubs this (returns NotImplemented); production wiring
//     plugs in a billing-svc REST adapter.
//
// Per the Wave 115 brief: NO Go imports of internal/billing or
// internal/enterprise. The cross-context types here are deliberately
// flat to keep that constraint honest.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
)

// =====================================================================
// 1. InvoiceSnapshot repo
// =====================================================================

type InvoiceSnapshotRepository interface {
	Create(ctx context.Context, snap *domain.InvoiceSnapshot) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.InvoiceSnapshot, error)
	ListByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.InvoiceSnapshot, error)
	// FindLatestByInvoice returns the most recent snapshot (highest
	// snapshotted_at) for an invoice, or nil if none exists. Drives the
	// "do we need to backfill?" check in SnapshotBackfillScan.
	FindLatestByInvoice(ctx context.Context, invoiceID uuid.UUID) (*domain.InvoiceSnapshot, error)
	ExistsForInvoice(ctx context.Context, invoiceID uuid.UUID) (bool, error)
}

// =====================================================================
// 2. CreditNote repo
// =====================================================================

type CreditNoteFilter struct {
	InvoiceID  *uuid.UUID
	CustomerID *uuid.UUID
	Status     string
	Limit      int
	Offset     int
}

type CreditNoteRepository interface {
	Create(ctx context.Context, cn *domain.CreditNote) error
	Update(ctx context.Context, cn *domain.CreditNote) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error)
	List(ctx context.Context, f CreditNoteFilter) ([]domain.CreditNote, int, error)
	// NextCreditNumber reserves a fresh credit_no via a DB sequence /
	// counter row. Caller is the Issue path — failure is fatal (we don't
	// fall back to a client-generated id, since credit numbers must be
	// monotonic per regulatory audit).
	NextCreditNumber(ctx context.Context) (string, error)
}

// =====================================================================
// 3. Bulk job + item repos
// =====================================================================

type BulkJobFilter struct {
	Kind   string
	Status string
	Limit  int
	Offset int
}

type BulkJobRepository interface {
	Create(ctx context.Context, j *domain.BulkGenerationJob) error
	Update(ctx context.Context, j *domain.BulkGenerationJob) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkGenerationJob, error)
	List(ctx context.Context, f BulkJobFilter) ([]domain.BulkGenerationJob, int, error)
	// ListPending returns jobs in 'pending' status, oldest first. Used
	// by the BulkJobRunner cron pickup loop.
	ListPending(ctx context.Context, limit int) ([]domain.BulkGenerationJob, error)
}

type BulkItemRepository interface {
	// CreateBatch is a bulk-insert helper used at job-queue time. Items
	// should arrive with Status=='queued' — the repo enforces it.
	CreateBatch(ctx context.Context, items []domain.BulkGenerationItem) error
	Update(ctx context.Context, item *domain.BulkGenerationItem) error
	ListByJob(ctx context.Context, jobID uuid.UUID) ([]domain.BulkGenerationItem, error)
	ListQueuedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkGenerationItem, error)
}

// =====================================================================
// 4. InvoiceReader — cross-context read port
//
// Pure SQL adapter against `billing.invoices` (and optionally
// `enterprise.invoices`). InvoiceProjection is the lowest-common-
// denominator shape so the usecase + handler don't need to know which
// upstream module produced the invoice.
// =====================================================================

// InvoiceProjection is the flat, source-agnostic shape returned by all
// cross-context invoice reads. amount_paid + outstanding are computed
// at the adapter level so the usecase doesn't repeat the sum-confirmed-
// payments math.
type InvoiceProjection struct {
	ID                uuid.UUID
	InvoiceNumber     string
	CustomerID        uuid.UUID
	OrderID           *uuid.UUID
	InvoiceType       string
	InvoiceDate       time.Time
	DueDate           time.Time
	Subtotal          float64
	PPNAmount         float64
	Total             float64
	Status            string
	PaidAt            *time.Time
	AmountPaid        float64
	Outstanding       float64
	PaymentMethod     string
	SourceModule      string // 'billing' | 'enterprise'
	CreatedAt         time.Time
}

type InvoiceQueryFilter struct {
	CustomerID *uuid.UUID
	Status     string
	From       *time.Time
	To         *time.Time
	PlanID     *uuid.UUID
	BranchID   *uuid.UUID
	CycleID    *uuid.UUID
	// Source filters down to a single upstream module. Empty = both.
	Source string
	Limit  int
	Offset int
}

type InvoiceReader interface {
	FindByID(ctx context.Context, id uuid.UUID) (*InvoiceProjection, error)
	ListByCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]InvoiceProjection, int, error)
	ListByCycle(ctx context.Context, cycleID uuid.UUID) ([]InvoiceProjection, error)
	// FindForBulkRun returns the customer set a bulk job should
	// process. The implementation reads the upstream module's tables
	// using the filter as a free-form set of predicates (cycle_id,
	// branch_id, customer_type, …).
	FindForBulkRun(ctx context.Context, filter map[string]any) ([]uuid.UUID, error)
	// Aggregations returns the dashboard rollup; the SQL is heavy and
	// lives at the adapter so the usecase stays simple.
	Aggregations(ctx context.Context, f InvoiceQueryFilter) (*AggregationResult, error)
	// CycleHealth returns generation health for one cycle (TC-IMD-004).
	CycleHealth(ctx context.Context, cycleID uuid.UUID) (*CycleHealthResult, error)
	// TopOverdueCustomers — TC-IMD-006 drill-down anchor.
	TopOverdueCustomers(ctx context.Context, limit int) ([]TopOverdueRow, error)
	// PaymentHistory — flat list of confirmed payments for a customer
	// (TC-IMC-003).
	PaymentHistory(ctx context.Context, customerID uuid.UUID, limit int) ([]PaymentHistoryRow, error)
	// ReminderHistory — flat list of dispatched reminders for a customer
	// (TC-IMC-004). Reads billing.reminder_log joined on invoices.
	ReminderHistory(ctx context.Context, customerID uuid.UUID, limit int) ([]ReminderHistoryRow, error)
	// IssuedInLast24h drives the snapshot backfill cron — returns
	// invoice IDs issued in the last window that don't yet have a
	// snapshot row.
	IssuedInLast24h(ctx context.Context, limit int) ([]uuid.UUID, error)
}

// =====================================================================
// 5. Monitoring projections
// =====================================================================

// AgingBuckets — TC-IMD-001 outstanding aging.
type AgingBuckets struct {
	Bucket0_30  float64 `json:"bucket_0_30"`
	Bucket31_60 float64 `json:"bucket_31_60"`
	Bucket61_90 float64 `json:"bucket_61_90"`
	Bucket91Up  float64 `json:"bucket_91_up"`
}

// AggregationResult — TC-IMD-001 + TC-IMD-002 dashboard top card.
type AggregationResult struct {
	TotalCount     int
	TotalAmount    float64
	PaidCount      int
	PaidAmount     float64
	OverdueCount   int
	OverdueAmount  float64
	IssuedCount    int
	IssuedAmount   float64
	CreditedCount  int
	AgingBuckets   AgingBuckets
	ByStatus       map[string]int
}

// CycleHealthResult — TC-IMD-004 generation health.
type CycleHealthResult struct {
	CycleID         uuid.UUID
	LastRunAt       *time.Time
	SuccessCount    int
	FailureCount    int
	AvgLatencyMS    float64
	StaleBy24h      bool
}

// TopOverdueRow — dashboard ranking item.
type TopOverdueRow struct {
	CustomerID         uuid.UUID
	CustomerName       string
	OverdueAmount      float64
	OldestOverdueDays  int
	InvoiceCount       int
}

// PaymentHistoryRow — TC-IMC-003.
type PaymentHistoryRow struct {
	ID            uuid.UUID
	InvoiceID     uuid.UUID
	Amount        float64
	Method        string
	GatewayRef    string
	PaymentDate   time.Time
	Status        string
}

// ReminderHistoryRow — TC-IMC-004.
type ReminderHistoryRow struct {
	ID         uuid.UUID
	InvoiceID  uuid.UUID
	Kind       string
	Channel    string
	SentAt     time.Time
	Delivered  *bool
	ErrorMsg   string
}

// =====================================================================
// 6. InvoiceGenerator — cross-context write port (stubbed by default)
// =====================================================================

// GenerationKind mirrors BulkJobKind in plain-text form so the
// cross-context call is JSON-friendly.
type GenerationKind string

const (
	GenerationKindMonthly    GenerationKind = "monthly_cycle"
	GenerationKindAddOn      GenerationKind = "add_on"
	GenerationKindAdjustment GenerationKind = "adjustment"
	GenerationKindCorrection GenerationKind = "correction"
)

type GeneratedInvoice struct {
	InvoiceID     uuid.UUID
	InvoiceNumber string
	Total         float64
}

type InvoiceGenerator interface {
	// GenerateForCustomer issues a fresh invoice for the given
	// customer + cycle. cycleID may be uuid.Nil for non-cycle kinds
	// (add_on, adjustment, correction).
	GenerateForCustomer(
		ctx context.Context,
		customerID uuid.UUID,
		cycleID uuid.UUID,
		kind GenerationKind,
	) (*GeneratedInvoice, error)
}

// =====================================================================
// 7. UseCase driving contracts
// =====================================================================

type SnapshotUseCase interface {
	CreateSnapshot(ctx context.Context, invoiceID uuid.UUID, lines []domain.SnapshotLineItem, schemaSnapshotID *uuid.UUID) (*domain.InvoiceSnapshot, error)
	GetSnapshot(ctx context.Context, id uuid.UUID) (*domain.InvoiceSnapshot, error)
	ListSnapshots(ctx context.Context, invoiceID uuid.UUID) ([]domain.InvoiceSnapshot, error)
}

type CreditNoteUseCase interface {
	Create(ctx context.Context, invoiceID uuid.UUID, customerID *uuid.UUID, amount float64, reason string, createdBy *uuid.UUID) (*domain.CreditNote, error)
	Issue(ctx context.Context, id uuid.UUID, approvedBy *uuid.UUID) (*domain.CreditNote, error)
	Apply(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error)
	Void(ctx context.Context, id uuid.UUID, by *uuid.UUID, reason string) (*domain.CreditNote, error)
	List(ctx context.Context, f CreditNoteFilter) ([]domain.CreditNote, int, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error)
}

type StartBulkJobInput struct {
	Kind         domain.BulkJobKind
	TargetFilter map[string]any
	CreatedBy    *uuid.UUID
}

type BulkUseCase interface {
	StartJob(ctx context.Context, in StartBulkJobInput) (*domain.BulkGenerationJob, error)
	RunJob(ctx context.Context, id uuid.UUID) (*domain.BulkGenerationJob, error)
	JobStatus(ctx context.Context, id uuid.UUID) (*domain.BulkGenerationJob, []domain.BulkGenerationItem, error)
	List(ctx context.Context, f BulkJobFilter) ([]domain.BulkGenerationJob, int, error)
}

type CustomerInvoiceFilter struct {
	Status string
	Limit  int
	Offset int
}

type MonitoringUseCase interface {
	// Customer-side.
	MyInvoices(ctx context.Context, customerID uuid.UUID, f CustomerInvoiceFilter) ([]InvoiceProjection, int, error)
	MyInvoice(ctx context.Context, customerID, invoiceID uuid.UUID) (*InvoiceProjection, error)
	MyPaymentHistory(ctx context.Context, customerID uuid.UUID, limit int) ([]PaymentHistoryRow, error)
	MyReminderHistory(ctx context.Context, customerID uuid.UUID, limit int) ([]ReminderHistoryRow, error)

	// Dashboard-side.
	Aggregations(ctx context.Context, f InvoiceQueryFilter) (*AggregationResult, error)
	CycleHealth(ctx context.Context, cycleID uuid.UUID) (*CycleHealthResult, error)
	TopOverdueCustomers(ctx context.Context, limit int) ([]TopOverdueRow, error)
}
