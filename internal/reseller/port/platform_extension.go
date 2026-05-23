package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
)

// =====================================================================
// Subscriber / invoice / import inputs
// =====================================================================

// CreateSubscriberInput is the platform-tenant-scoped create. The
// usecase pulls the tenant from request context; callers MUST set
// ResellerAccountID to the same value (the usecase validates the
// match and returns Forbidden on mismatch). This belt-and-braces
// check is what blocks a tampered request body from creating a row
// under a different tenant.
type CreateSubscriberInput struct {
	ResellerAccountID uuid.UUID
	CustomerName      string
	CustomerEmail     string
	CustomerPhone     string
	AddressLine       string
	SubAreaID         *uuid.UUID
	ServicePlanID     *uuid.UUID
	MonthlyFee        float64
	Notes             string
}

// UpdateSubscriberInput allows partial field updates. Nil pointers
// mean "leave unchanged"; status changes go through the dedicated
// Suspend/Reactivate/Terminate methods so the state machine isn't
// bypassed here.
type UpdateSubscriberInput struct {
	CustomerName  *string
	CustomerEmail *string
	CustomerPhone *string
	AddressLine   *string
	SubAreaID     *uuid.UUID
	ServicePlanID *uuid.UUID
	MonthlyFee    *float64
	Notes         *string
}

// SubscriberListFilter — every List/Find on this filter REQUIRES a
// non-nil ResellerAccountID. The postgres adapter refuses queries
// where ResellerAccountID == uuid.Nil so a missing tenant filter is
// a defect rather than a leak.
type SubscriberListFilter struct {
	ResellerAccountID uuid.UUID
	Status            string
	Limit             int
	Offset            int
}

// InvoiceListFilter — same tenant-required rule as subscriber filter.
type InvoiceListFilter struct {
	ResellerAccountID uuid.UUID
	SubscriberID      *uuid.UUID
	Status            string
	PeriodYear        int
	PeriodMonth       int
	Limit             int
	Offset            int
}

// SubscriberImportListFilter — for the future "show me my past CSV
// uploads" surface.
type SubscriberImportListFilter struct {
	ResellerAccountID uuid.UUID
	Status            string
	Limit             int
	Offset            int
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================
//
// CRITICAL: every List/Find method MUST require a non-nil
// reseller_account_id filter. The postgres adapters refuse queries
// without one — a missing filter is a defect, not a 0-row result.

type SubscriberRepository interface {
	Create(ctx context.Context, s *domain.Subscriber) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error)
	// FindForReseller is the tenant-scoped lookup. The repo refuses
	// uuid.Nil for resellerID. Returns NotFound when the id doesn't
	// belong to this tenant — the leak path is closed.
	FindForReseller(ctx context.Context, resellerID, id uuid.UUID) (*domain.Subscriber, error)
	List(ctx context.Context, f SubscriberListFilter) ([]domain.Subscriber, int, error)
	Update(ctx context.Context, s *domain.Subscriber) error
	UpdateStatus(ctx context.Context, s *domain.Subscriber) error
	Count(ctx context.Context, f SubscriberListFilter) (int, error)
}

type SubscriberInvoiceRepository interface {
	Create(ctx context.Context, i *domain.SubscriberInvoice) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error)
	FindForReseller(ctx context.Context, resellerID, id uuid.UUID) (*domain.SubscriberInvoice, error)
	List(ctx context.Context, f InvoiceListFilter) ([]domain.SubscriberInvoice, int, error)
	UpdateStatus(ctx context.Context, i *domain.SubscriberInvoice) error
	// ListOverdueForReseller is the dashboard helper: enumerate the
	// open invoices whose due_at is past `asOf`. The dashboard does
	// NOT flip them in DB — it computes the count on the fly so the
	// admin/operator drives the actual MarkOverdue transition.
	ListOverdueForReseller(ctx context.Context, resellerID uuid.UUID, asOf time.Time) ([]domain.SubscriberInvoice, error)
	// SumPaidMTD returns the sum of `amount` for paid invoices whose
	// paid_at falls within [monthStart, asOf]. Used by the dashboard
	// "invoices_paid_mtd" tile.
	SumPaidMTD(ctx context.Context, resellerID uuid.UUID, monthStart, asOf time.Time) (float64, error)
	// SumOpen returns the sum of `amount` for invoices in status open.
	// Drives the "invoices_open_total" tile.
	SumOpen(ctx context.Context, resellerID uuid.UUID) (float64, error)
}

type SubscriberImportRepository interface {
	Create(ctx context.Context, im *domain.SubscriberImport) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SubscriberImport, error)
	UpdateStatus(ctx context.Context, im *domain.SubscriberImport) error
	List(ctx context.Context, f SubscriberImportListFilter) ([]domain.SubscriberImport, int, error)
}

// =====================================================================
// Cross-context: read-only access to partnership.compliance_evaluations
// =====================================================================
//
// The reseller-platform MTD dashboard surfaces a "compliance status"
// chip ("on_track" / "at_risk" / "ramp") computed from the latest
// compliance evaluation row for the tenant. The partnership context
// owns that data — we stay loosely coupled by reading it through a
// thin adapter (internal/reseller/adapter/partnership/compliance_reader.go)
// that issues a direct SQL query against `partnership.compliance_evaluations`.
//
// The adapter is the ONLY approved cross-context reference in this
// wave. No Go imports cross the bounded-context line — the adapter
// only touches a separate schema and returns a small DTO.
//
// If migration 0066 (partnership) hasn't applied yet, the adapter
// returns derrors.Wrap(KindUnavailable, "partnership.compliance.not_available", ...).
// The dashboard handles that gracefully (compliance_status: "unavailable").

// ComplianceSnapshot is the slim DTO the dashboard needs. We keep it
// in the port package (not domain) because it isn't a reseller-context
// entity — it's a view over an external table.
type ComplianceSnapshot struct {
	Status       string    // "ramp_skipped" | "passed" | "breached"
	AchievedPct  float64   // 0..1
	ThresholdPct float64   // 0..1
	EvaluatedAt  time.Time
}

// ComplianceReader returns the latest compliance evaluation for a
// reseller. Returns NotFound when no evaluation exists yet (e.g.
// brand-new reseller). Returns KindUnavailable when the partnership
// schema isn't reachable (migration not applied / connectivity).
type ComplianceReader interface {
	LatestForReseller(ctx context.Context, resellerID uuid.UUID) (*ComplianceSnapshot, error)
}

// =====================================================================
// Wave 102 service interfaces (driving ports)
// =====================================================================
//
// These are exposed to the HTTP layer; the implementations live in
// internal/reseller/usecase/platform_*.go. The interfaces sit here so
// tests can stub them per-route.

type SubscriberUseCase interface {
	CreateSubscriber(ctx context.Context, in CreateSubscriberInput) (*domain.Subscriber, error)
	ListMySubscribers(ctx context.Context, f SubscriberListFilter) ([]domain.Subscriber, int, error)
	GetMySubscriber(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error)
	UpdateMySubscriber(ctx context.Context, id uuid.UUID, fields UpdateSubscriberInput) (*domain.Subscriber, error)
	SuspendMySubscriber(ctx context.Context, id uuid.UUID, reason string) (*domain.Subscriber, error)
	ReactivateMySubscriber(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error)
	TerminateMySubscriber(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error)
	ImportSubscribersCSV(ctx context.Context, csvBytes []byte) (*domain.SubscriberImport, error)
}

type InvoiceInboxUseCase interface {
	ListMyInvoices(ctx context.Context, f InvoiceListFilter) ([]domain.SubscriberInvoice, int, error)
	GetMyInvoice(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error)
	MarkMyInvoicePaid(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error)
	OverdueAtMonthEnd(ctx context.Context, asOf time.Time) ([]domain.SubscriberInvoice, error)
}

// MTDDashboard is the structured payload behind GET
// /api/platform/dashboard/mtd. The DashboardService scopes every read
// to the resolved tenant.
//
// compliance_status:
//
//	"on_track"     — latest eval is `passed`
//	"at_risk"      — latest eval is `breached`
//	"ramp"         — latest eval is `ramp_skipped` (still in grace window)
//	"unavailable"  — partnership data not reachable
//	"unknown"      — no evaluation row exists yet
type MTDDashboard struct {
	SubscribersTotal          int     `json:"subscribers_total"`
	SubscribersActive         int     `json:"subscribers_active"`
	SubscribersAddedMTD       int     `json:"subscribers_added_mtd"`
	SubscribersTerminatedMTD  int     `json:"subscribers_terminated_mtd"`
	InvoicesOpenTotal         float64 `json:"invoices_open_total"`
	InvoicesOverdueTotal      float64 `json:"invoices_overdue_total"`
	InvoicesPaidMTD           float64 `json:"invoices_paid_mtd"`
	ComplianceStatus          string  `json:"compliance_status"`
	ComplianceAchievedPct     float64 `json:"compliance_achieved_pct"`
	ComplianceThresholdPct    float64 `json:"compliance_threshold_pct"`
	ComplianceEvaluatedAt     *time.Time `json:"compliance_evaluated_at,omitempty"`
}

type DashboardUseCase interface {
	MTD(ctx context.Context) (*MTDDashboard, error)
}
