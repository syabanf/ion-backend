// Package domain holds the HRIS bounded context's entities and value objects.
//
// HRIS is the source of truth for employment status. Other contexts subscribe
// to HRIS events:
//   - identity: deactivate user on resign
//   - billing/commission: cancel pending triggers for resigned sales reps
//   - field: skip on-leave / suspended technicians during WO dispatch
//
// Wave 118: minimal core sufficient for the 12 TC-HRIS-* test cases. Future
// waves (Wave 119 Schema Commission Deep, Wave 122 NFR) will extend with
// attendance + payroll bridges.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// EmployeeStatus is the employment status enum, mirrored by the migration's
// CHECK constraint. State transitions live on Employee methods below.
type EmployeeStatus string

const (
	EmployeeStatusProbation EmployeeStatus = "probation"
	EmployeeStatusActive    EmployeeStatus = "active"
	EmployeeStatusResigned  EmployeeStatus = "resigned"
	EmployeeStatusSuspended EmployeeStatus = "suspended"
)

// Valid reports whether the status string is one of the enumerated values.
func (s EmployeeStatus) Valid() bool {
	switch s {
	case EmployeeStatusProbation, EmployeeStatusActive,
		EmployeeStatusResigned, EmployeeStatusSuspended:
		return true
	}
	return false
}

// Employee is the HRIS aggregate root. employee_no is the canonical FK
// target (NOT id — id is the internal surrogate). Other contexts that
// reference an employee should use employee_no.
type Employee struct {
	ID                   uuid.UUID
	EmployeeNo           string
	FullName             string
	Email                string
	Phone                string
	Department           string
	Position             string
	ManagerEmployeeNo    string
	HireDate             *time.Time
	ResignDate           *time.Time
	Status               EmployeeStatus
	KYCCompleted         bool
	NPWP                 string
	BankAccountNo        string
	BranchID             *uuid.UUID
	RoleRecommendations  []string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewEmployee constructs an Employee. Enforces non-empty employee_no and
// full_name. The status defaults to probation per HR PRD §2; callers can
// promote to active via Hire(at).
func NewEmployee(employeeNo, fullName string) (*Employee, error) {
	employeeNo = strings.TrimSpace(employeeNo)
	fullName = strings.TrimSpace(fullName)
	if employeeNo == "" {
		return nil, errors.Validation("hris.employee_no_required", "employee_no is required")
	}
	if fullName == "" {
		return nil, errors.Validation("hris.full_name_required", "full_name is required")
	}
	now := time.Now().UTC()
	return &Employee{
		ID:         uuid.New(),
		EmployeeNo: employeeNo,
		FullName:   fullName,
		Status:     EmployeeStatusProbation,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Hire transitions Probation → Active. No-op if already active. Rejects
// terminal states (resigned).
func (e *Employee) Hire(at time.Time) error {
	switch e.Status {
	case EmployeeStatusProbation:
		e.Status = EmployeeStatusActive
		if e.HireDate == nil {
			h := at
			e.HireDate = &h
		}
		e.UpdatedAt = time.Now().UTC()
		return nil
	case EmployeeStatusActive:
		return nil
	default:
		return errors.Conflict("hris.invalid_transition",
			"cannot hire employee in status "+string(e.Status))
	}
}

// Promote updates the position string. No status change. Rejects terminal
// states (resigned).
func (e *Employee) Promote(newPosition string) error {
	newPosition = strings.TrimSpace(newPosition)
	if newPosition == "" {
		return errors.Validation("hris.position_required", "new position is required")
	}
	if e.Status == EmployeeStatusResigned {
		return errors.Conflict("hris.invalid_transition",
			"cannot promote resigned employee")
	}
	e.Position = newPosition
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Resign transitions Active / Probation / Suspended → Resigned. Idempotent
// on re-call (no-op if already resigned). The resign date is captured for
// commission-cessation lookups in Wave 114's orchestration tick.
func (e *Employee) Resign(at time.Time, _ string) error {
	if e.Status == EmployeeStatusResigned {
		return nil
	}
	e.Status = EmployeeStatusResigned
	rd := at
	e.ResignDate = &rd
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Suspend transitions Active → Suspended. Used for HR-initiated discipline
// holds. Reversible via Reinstate.
func (e *Employee) Suspend(_ string) error {
	switch e.Status {
	case EmployeeStatusActive, EmployeeStatusProbation:
		e.Status = EmployeeStatusSuspended
		e.UpdatedAt = time.Now().UTC()
		return nil
	case EmployeeStatusSuspended:
		return nil
	default:
		return errors.Conflict("hris.invalid_transition",
			"cannot suspend employee in status "+string(e.Status))
	}
}

// Reinstate transitions Suspended → Active. No-op if already active.
func (e *Employee) Reinstate() error {
	switch e.Status {
	case EmployeeStatusSuspended:
		e.Status = EmployeeStatusActive
		e.UpdatedAt = time.Now().UTC()
		return nil
	case EmployeeStatusActive:
		return nil
	default:
		return errors.Conflict("hris.invalid_transition",
			"cannot reinstate employee in status "+string(e.Status))
	}
}

// IsResignedBefore reports whether the employee had ALREADY resigned by
// the given moment t. Returns true when status=resigned AND resign_date <= t.
//
// Used by Wave 114's commission-trigger evaluator: an invoice paid AFTER
// the sales rep resigned must not pay them commission. The end-of-day
// comparison means a payment on the resign date itself still counts
// against the rep (they were employed that day, so commission may be due
// — Schema Commission Deep / Wave 119 will refine clawback semantics).
func (e *Employee) IsResignedBefore(t time.Time) bool {
	if e == nil || e.Status != EmployeeStatusResigned || e.ResignDate == nil {
		return false
	}
	// ResignDate is a date-only value. End-of-resign-day is the last
	// instant the rep was still employed; anything strictly after that
	// means the rep had already left when the invoice was paid.
	rd := *e.ResignDate
	endOfResignDay := time.Date(rd.Year(), rd.Month(), rd.Day(), 23, 59, 59, 0, time.UTC)
	return t.After(endOfResignDay)
}
