// Package port defines the HRIS bounded context's ports — repositories
// (persistence) and gateways (external integrations) — that the use cases
// depend on. The cmd/hris-svc binary wires concrete adapters from
// internal/hris/adapter/* into these interfaces.
//
// Cross-context bridges (CommissionCessationHook, UserDeactivator) live
// here too — they are narrow, intention-revealing interfaces that the
// svc binary satisfies via tiny inline SQL adapters. This keeps the
// hris/ package free of cross-context imports.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/hris/domain"
)

// =====================================================================
// Persistence — one repo per aggregate
// =====================================================================

// EmployeeFilter is the projection used by Search / List.
type EmployeeFilter struct {
	Query      string
	Status     domain.EmployeeStatus
	BranchID   *uuid.UUID
	Department string
	Limit      int
	Offset     int
}

// EmployeeRepository persists hris.employees rows. Upsert is keyed on
// employee_no — the sync flow re-runs without producing duplicates.
type EmployeeRepository interface {
	Upsert(ctx context.Context, e *domain.Employee) error
	FindByEmployeeNo(ctx context.Context, employeeNo string) (*domain.Employee, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Employee, error)
	List(ctx context.Context, f EmployeeFilter) ([]domain.Employee, int, error)
}

// EventFilter is the projection used by ListEvents.
type EventFilter struct {
	EmployeeNo string
	Kind       domain.EventKind
	Processed  *bool
	Limit      int
	Offset     int
}

// EventRepository persists hris.employee_events rows. CreateMany must
// dedupe on (id) — events arriving from the gateway carry a stable id
// so a re-poll doesn't duplicate the queue.
type EventRepository interface {
	CreateMany(ctx context.Context, events []*domain.EmployeeEvent) (insertedNew int, err error)
	ListPending(ctx context.Context, limit int) ([]domain.EmployeeEvent, error)
	List(ctx context.Context, f EventFilter) ([]domain.EmployeeEvent, int, error)
	MarkProcessed(ctx context.Context, id uuid.UUID, processingError string) error
}

// =====================================================================
// External — HRIS gateway
// =====================================================================

// EmployeeRecord is the gateway-side projection. The adapter maps this
// to/from the HRIS counterparty's wire format (REST / SOAP / CSV-poll).
type EmployeeRecord struct {
	EmployeeNo          string
	FullName            string
	Email               string
	Phone               string
	Department          string
	Position            string
	ManagerEmployeeNo   string
	HireDate            *time.Time
	ResignDate          *time.Time
	Status              domain.EmployeeStatus
	KYCCompleted        bool
	NPWP                string
	BankAccountNo       string
	BranchID            *uuid.UUID
	RoleRecommendations []string
}

// HRISGateway is the external HRIS connector. The stub implementation
// returns canned data; a real adapter polls or consumes a webhook.
type HRISGateway interface {
	FetchEmployees(ctx context.Context, since time.Time) ([]EmployeeRecord, error)
	FetchEvents(ctx context.Context, since time.Time) ([]*domain.EmployeeEvent, error)
}

// =====================================================================
// Cross-context bridges — narrow, intention-revealing
// =====================================================================

// CommissionCessationHook is called when an employee resigns. The svc
// binary wires it to a tiny inline SQL adapter that cancels pending
// rows in crm.commissions / billing.commission_triggers.
type CommissionCessationHook interface {
	OnResign(ctx context.Context, employeeNo string, resignDate time.Time) error
}

// UserDeactivator is called when an employee resigns / suspended. The
// svc binary wires it to identity (flip is_active=false on the user
// whose hris_employee_no matches).
type UserDeactivator interface {
	DeactivateByEmployeeNo(ctx context.Context, employeeNo string) error
}

// FieldQueueReassigner is called when an employee transfers. Wave 118
// keeps this as an audit-only hook — the actual reassignment is a manual
// Ops task today. The hook is invoked so the audit captures the intent.
type FieldQueueReassigner interface {
	OnTransfer(ctx context.Context, employeeNo string) error
}

// RBACRecalculator is called when an employee's role changes. The svc
// binary wires it to identity to recompute the user's effective grants.
type RBACRecalculator interface {
	OnRoleChange(ctx context.Context, employeeNo string) error
}

// =====================================================================
// HRISResignedReader — port consumed by Wave 114 billing orchestration
// =====================================================================

// HRISResignedReader is the read-side projection that internal/billing's
// orchestration tick consults before queuing a commission trigger. A
// resigned sales rep should not earn commission on an invoice paid after
// their resign date.
//
// Implementations are nil-safe — Wave 114 callers check for nil first.
type HRISResignedReader interface {
	// IsResignedBefore returns true iff the user identified by
	// salesUserID maps to an employee whose status=resigned AND
	// resign_date <= t.
	IsResignedBefore(ctx context.Context, salesUserID uuid.UUID, t time.Time) bool
}
