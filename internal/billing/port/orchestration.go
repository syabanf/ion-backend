// Wave 114 — Billing orchestration ports.
//
// The five new evaluators (reminder, late-fee, suspension, restore-
// on-paid, commission-trigger) each need:
//
//   * a repository to persist their per-tick log row (reminder_log,
//     late_fee_applications, suspension_actions, commission_triggers)
//   * a small narrow cross-context interface to bridge into existing
//     services (RADIUSRestorer, CustomerSuspender, ReminderDispatcher)
//
// We keep these contracts in their own file to avoid swelling port.go
// (which is already 380 LoC of M6 r1-r3 contracts). Nothing here
// reaches into another bounded context directly — the bridges live
// in cmd/billing-svc/main.go.

package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
)

// =====================================================================
// Repositories — one per Wave 114 log table.
// =====================================================================

// ReminderLogRow mirrors a billing.reminder_log row for the purposes
// of the cron evaluator's lookup. The dispatcher contract returns a
// messageID + delivered flag which the repo captures here.
type ReminderLogRow struct {
	ID         uuid.UUID
	InvoiceID  uuid.UUID
	Kind       domain.ReminderKind
	SentAt     time.Time
	Channel    string
	Delivered  *bool
	MessageID  string
	ErrorMsg   string
}

// ReminderLogFilter scopes a ListPending search; today only the
// invoice-id filter is wired (the cron evaluator fetches one row at
// a time per invoice).
type ReminderLogFilter struct {
	InvoiceID *uuid.UUID
	Kind      domain.ReminderKind
	Limit     int
}

// ReminderLogRepository persists the per-(invoice, kind) dispatch
// row. UNIQUE (invoice_id, kind) means Create must use ON CONFLICT DO
// NOTHING semantics so the cron can be safely retried — the second
// caller into a race just returns the existing row.
type ReminderLogRepository interface {
	Create(ctx context.Context, row *ReminderLogRow) error
	FindLastByInvoice(ctx context.Context, invoiceID uuid.UUID) (*ReminderLogRow, error)
	ListPending(ctx context.Context, f ReminderLogFilter) ([]ReminderLogRow, error)
}

// LateFeeApplicationRow mirrors a billing.late_fee_applications row.
type LateFeeApplicationRow struct {
	ID              uuid.UUID
	InvoiceID       uuid.UUID
	SchemaVersionID *uuid.UUID
	AppliedAmount   float64
	AppliedAt       time.Time
	Basis           string
	UndoAt          *time.Time
	UndoReason      string
}

// LateFeeApplicationRepository persists one row per (invoice). The
// repo's Create implementation MUST use ON CONFLICT (invoice_id) DO
// NOTHING so the cron stays idempotent on re-run.
//
// CreatedNew returns true when Create actually inserted a new row;
// false on no-op (ON CONFLICT path). The cron uses that signal to
// decide whether to bump the invoice's total.
type LateFeeApplicationRepository interface {
	Create(ctx context.Context, row *LateFeeApplicationRow) (createdNew bool, err error)
	FindByInvoice(ctx context.Context, invoiceID uuid.UUID) (*LateFeeApplicationRow, error)
	Undo(ctx context.Context, invoiceID uuid.UUID, reason string) error
}

// SuspensionActionRow mirrors a billing.suspension_actions row.
type SuspensionActionRow struct {
	ID                    uuid.UUID
	CustomerID            uuid.UUID
	TriggeredByInvoiceID  *uuid.UUID
	SchemaVersionID       *uuid.UUID
	Action                domain.SuspensionActionKind
	ExecutedAt            time.Time
	GraceWindowHours      *int
	ExecutedBy            string
}

// SuspensionActionFilter scopes ListByActionInWindow.
type SuspensionActionFilter struct {
	Action domain.SuspensionActionKind
	From   time.Time
	To     time.Time
	Limit  int
}

type SuspensionActionRepository interface {
	Create(ctx context.Context, row *SuspensionActionRow) error
	FindLastByCustomer(ctx context.Context, customerID uuid.UUID) (*SuspensionActionRow, error)
	ListByActionInWindow(ctx context.Context, f SuspensionActionFilter) ([]SuspensionActionRow, error)
}

// CommissionTriggerRow mirrors a billing.commission_triggers row.
type CommissionTriggerRow struct {
	ID                      uuid.UUID
	PlanChangeID            *uuid.UUID
	CustomerID              *uuid.UUID
	SalesUserID             *uuid.UUID
	TriggerKind             domain.CommissionTriggerKind
	InvoiceID               *uuid.UUID
	AmountBasis             *float64
	SchemaVersionID         *uuid.UUID
	FiredAt                 time.Time
	CommissionAmount        *float64
	CommissionRecipientID   *uuid.UUID
}

type CommissionTriggerRepository interface {
	// Create writes one row; the repo MUST use ON CONFLICT
	// (plan_change_id, trigger_kind) DO NOTHING so the cron stays
	// idempotent. Returns true when a new row landed.
	Create(ctx context.Context, row *CommissionTriggerRow) (createdNew bool, err error)
	ListByPlanChange(ctx context.Context, planChangeID uuid.UUID) ([]CommissionTriggerRow, error)
	ListByCustomer(ctx context.Context, customerID uuid.UUID) ([]CommissionTriggerRow, error)
}

// =====================================================================
// Cross-context bridges.
//
// Each is a narrow interface (one method) so cmd/billing-svc can wire
// real implementations — or no-op stubs — without dragging in the
// other bounded context's domain.
// =====================================================================

// RADIUSRestorer flips a customer's RADIUS session back to ACTIVE.
// Implementations live in cmd/billing-svc/main.go and call into
// internal/network's RADIUS client. The restore-on-paid evaluator
// calls this AFTER the CustomerSuspender flip but BEFORE writing the
// 'restore' row to billing.suspension_actions — so an audit reader
// sees the cause-and-effect chain in the right order.
type RADIUSRestorer interface {
	RestoreCustomer(ctx context.Context, customerID uuid.UUID) error
}

// CustomerSuspender bridges to the CRM context's suspension state
// machine. The 'state' argument is one of domain.CustomerSuspensionState.
// Adapters typically delegate to crm/usecase.SetCustomerStatus (or
// the same internal/billing/adapter/crm gateway used elsewhere).
type CustomerSuspender interface {
	SetSuspensionState(ctx context.Context, customerID uuid.UUID, state domain.CustomerSuspensionState) error
}

// ReminderTarget is the projection the dispatcher needs about a
// customer to actually fan out a reminder. Built from the CRM
// gateway's CustomerSummary by the orchestration service.
type ReminderTarget struct {
	CustomerID   uuid.UUID
	CustomerName string
	PhoneE164    string
	Email        string
}

// ReminderInvoiceSnapshot is the invoice projection threaded to the
// dispatcher. Mirrors the fields a WhatsApp / email template renders.
type ReminderInvoiceSnapshot struct {
	InvoiceID         uuid.UUID
	InvoiceNumber     string
	Total             float64
	OutstandingAmount float64
	DueDate           time.Time
}

// ReminderDispatcher fans the actual reminder out via notifyx /
// WhatsApp / email. The messageID return is captured into
// billing.reminder_log for later auditing.
type ReminderDispatcher interface {
	SendReminder(
		ctx context.Context,
		target ReminderTarget,
		invoice ReminderInvoiceSnapshot,
		kind domain.ReminderKind,
		channel string,
	) (messageID string, err error)
}

// =====================================================================
// Customer reader — the narrow projection the orchestration usecase
// needs from CRM. Distinct from the existing CRMGateway so a wave-114
// caller doesn't have to wire the whole r2/r3 gateway just to read a
// customer name + phone.
// =====================================================================

// CustomerReader projects the minimal customer fields the
// orchestration evaluators need. Implementations typically wrap the
// existing internal/billing/adapter/crm gateway.
type CustomerReader interface {
	// ReadForReminder returns the projection used to render a
	// reminder template. Returns NotFound when the customer is gone.
	ReadForReminder(ctx context.Context, customerID uuid.UUID) (*ReminderTarget, error)
	// ListSuspensionCandidates returns customers with at least one
	// open / partial invoice past due, ordered by oldest-overdue
	// invoice's due_date ASC. The evaluator caps at `limit`.
	ListSuspensionCandidates(ctx context.Context, limit int) ([]SuspensionCandidate, error)
	// ListRestoreCandidates returns customers currently in
	// soft_suspend or hard_suspend with no remaining unpaid
	// invoices.
	ListRestoreCandidates(ctx context.Context, limit int) ([]RestoreCandidate, error)
}

// SuspensionCandidate is the projection the suspension evaluator
// loops over.
type SuspensionCandidate struct {
	CustomerID        uuid.UUID
	CurrentState      domain.CustomerSuspensionState
	OldestOverdueDue  time.Time
	OldestInvoiceID   *uuid.UUID
}

// RestoreCandidate is the projection the restore-on-paid evaluator
// loops over.
type RestoreCandidate struct {
	CustomerID    uuid.UUID
	CurrentState  domain.CustomerSuspensionState
	LastPaidAt    *time.Time
}

// =====================================================================
// Plan change reader — narrow projection for the commission-trigger
// evaluator. The evaluator needs (paid invoice, plan_change_id,
// sales_user_id) tuples; the existing CRM gateway doesn't expose
// plan_change_requests, so this is a Wave-114-only port.
// =====================================================================

// PlanChangePaidInvoice is the projection the commission-trigger
// evaluator loops over.
type PlanChangePaidInvoice struct {
	InvoiceID    uuid.UUID
	CustomerID   uuid.UUID
	PlanChangeID uuid.UUID
	SalesUserID  uuid.UUID
	AmountBasis  float64
	PaidAt       time.Time
	ActivatedAt  *time.Time
}

// PlanChangeReader projects the plan-change → paid-invoice join the
// commission-trigger evaluator needs. Implementations may query
// crm.plan_change_requests directly via the shared pool; that SQL-
// only cross-context query is acceptable per the same pattern the
// existing CRMGateway uses.
type PlanChangeReader interface {
	ListRecentlyPaidForCommission(ctx context.Context, since time.Time, limit int) ([]PlanChangePaidInvoice, error)
}

// =====================================================================
// HRIS resigned reader — Wave 118 add-on.
//
// Consulted by RunCommissionTriggerTick before queuing a commission
// trigger. A resigned sales rep should not earn commission on an
// invoice paid after their resign date (TC-SCD-001 / TC-HRI-006 link).
// Nil-safe — when the bridge is unwired the tick falls through to its
// existing behaviour.
// =====================================================================

// HRISResignedReader is the read-side projection that the commission
// trigger evaluator consults. Implementations may be backed by either
// (a) a direct cross-schema query into hris.employees joined to
// identity.users.hris_employee_no, or (b) the in-memory HRIS service
// when both modules run in the same process.
type HRISResignedReader interface {
	IsResignedBefore(ctx context.Context, salesUserID uuid.UUID, t time.Time) bool
}
