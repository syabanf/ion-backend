// Wave 118 — Employee event domain.
//
// EmployeeEvent is an immutable record of a change in employment state
// (hired / promoted / resigned / etc.). Events are produced by the HRIS
// gateway (real or stub), persisted to hris.employee_events, and consumed
// by the EventProcessor cron via ProcessHook.

package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// EventKind enumerates the supported employee event types, mirrored by the
// migration's CHECK constraint.
type EventKind string

const (
	EventKindHired         EventKind = "hired"
	EventKindTransferred   EventKind = "transferred"
	EventKindPromoted      EventKind = "promoted"
	EventKindResigned      EventKind = "resigned"
	EventKindSuspended     EventKind = "suspended"
	EventKindReinstated    EventKind = "reinstated"
	EventKindRoleChanged   EventKind = "role_changed"
	EventKindSalaryChanged EventKind = "salary_changed"
)

// Valid reports whether the kind string is one of the enumerated values.
func (k EventKind) Valid() bool {
	switch k {
	case EventKindHired, EventKindTransferred, EventKindPromoted,
		EventKindResigned, EventKindSuspended, EventKindReinstated,
		EventKindRoleChanged, EventKindSalaryChanged:
		return true
	}
	return false
}

// EmployeeEvent is a single ingest record. Payload is free-form JSON;
// per-kind shape is documented in the HRIS gateway contract.
type EmployeeEvent struct {
	ID              uuid.UUID
	EmployeeNo      string
	Kind            EventKind
	Payload         map[string]any
	OccurredAt      time.Time
	IngestedAt      time.Time
	Source          string
	Processed       bool
	ProcessedAt     *time.Time
	ProcessingError string
}

// NewEmployeeEvent constructs a not-yet-persisted event. Source defaults
// to "manual" if empty.
func NewEmployeeEvent(employeeNo string, kind EventKind, payload map[string]any, occurredAt time.Time, source string) (*EmployeeEvent, error) {
	employeeNo = strings.TrimSpace(employeeNo)
	if employeeNo == "" {
		return nil, errors.Validation("hris.event.employee_no_required", "employee_no is required")
	}
	if !kind.Valid() {
		return nil, errors.Validation("hris.event.kind_invalid", "event kind "+string(kind)+" is not supported")
	}
	if occurredAt.IsZero() {
		return nil, errors.Validation("hris.event.occurred_at_required", "occurred_at is required")
	}
	if source = strings.TrimSpace(source); source == "" {
		source = "manual"
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return &EmployeeEvent{
		ID:         uuid.New(),
		EmployeeNo: employeeNo,
		Kind:       kind,
		Payload:    payload,
		OccurredAt: occurredAt,
		IngestedAt: time.Now().UTC(),
		Source:     source,
	}, nil
}

// HookDirective is what ProcessHook returns. Cron callers inspect the flags
// and dispatch the relevant cross-context bridge (commission cessation,
// user deactivation, audit row, etc.). Designed so a missing bridge can
// be ignored without losing the event — the row gets marked processed
// either way.
type HookDirective struct {
	// CancelCommissions — fire for resigned events. Wave 114's commission
	// trigger evaluator should refuse to fire new triggers for this
	// employee after ResignDate (handled by HRISResignedReader gate).
	CancelCommissions bool

	// DeactivateUser — fire for resigned events. Bridges to identity to
	// flip the identity.users.is_active flag.
	DeactivateUser bool

	// ReassignFieldQueue — fire for transferred / role_changed events.
	// In Wave 118 we only audit the directive; the actual field-queue
	// reassignment is a manual Ops task (will be automated in a later wave).
	ReassignFieldQueue bool

	// UpdateRBAC — fire for role_changed events. Bridges to identity to
	// recompute the user's effective role set.
	UpdateRBAC bool

	// AuditOnly — set when no cross-context bridge fires (e.g. hired,
	// promoted, salary_changed). The audit row is still written.
	AuditOnly bool

	// AuditReason — short tag for the audit entry's Reason field.
	AuditReason string
}

// ProcessHook returns the directive for this event. Pure function — no
// side effects. The cron's EventService.ProcessPending consults this to
// decide which bridges to call.
func (e *EmployeeEvent) ProcessHook() HookDirective {
	if e == nil {
		return HookDirective{AuditOnly: true, AuditReason: "hris.event.nil"}
	}
	switch e.Kind {
	case EventKindResigned:
		return HookDirective{
			CancelCommissions: true,
			DeactivateUser:    true,
			AuditReason:       "hris.resigned",
		}
	case EventKindSuspended:
		return HookDirective{
			DeactivateUser: true,
			AuditReason:    "hris.suspended",
		}
	case EventKindReinstated:
		return HookDirective{
			AuditReason: "hris.reinstated",
		}
	case EventKindTransferred:
		return HookDirective{
			ReassignFieldQueue: true,
			AuditReason:        "hris.transferred",
		}
	case EventKindRoleChanged:
		return HookDirective{
			UpdateRBAC:  true,
			AuditReason: "hris.role_changed",
		}
	case EventKindHired, EventKindPromoted, EventKindSalaryChanged:
		return HookDirective{
			AuditOnly:   true,
			AuditReason: "hris." + string(e.Kind),
		}
	default:
		return HookDirective{AuditOnly: true, AuditReason: "hris.unknown"}
	}
}
