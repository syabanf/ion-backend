// Package usecase orchestrates the HRIS bounded context's flows.
//
// Three services, one file each:
//   - EmployeeService: CRUD + state transitions (resign, reinstate)
//   - EventService:    ingest + drain the event queue (this file's sibling)
//   - SyncService:     wraps the HRIS gateway end-to-end (sync.go)
//
// All services are nil-safe — every dependency is optional, with a structured
// warning when a required dep is missing.

package usecase

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// EmployeeService is the CRUD + state-transition surface for hris.employees.
type EmployeeService struct {
	employees   port.EmployeeRepository
	auditWriter audit.Writer
	log         *slog.Logger
}

// NewEmployeeService builds an EmployeeService. employees may be nil only
// in test scaffolds — production wiring requires a real repo.
func NewEmployeeService(employees port.EmployeeRepository, auditWriter audit.Writer, log *slog.Logger) *EmployeeService {
	if log == nil {
		log = slog.Default()
	}
	if auditWriter == nil {
		auditWriter = audit.Nop{}
	}
	return &EmployeeService{
		employees:   employees,
		auditWriter: auditWriter,
		log:         log.With("component", "hris.employee"),
	}
}

// Upsert is the idempotent create-or-update entry. Keyed on employee_no:
// the second call with the same employee_no updates the existing row
// (preserving id + created_at). Used by both the manual admin HTTP path
// and the SyncService.
func (s *EmployeeService) Upsert(ctx context.Context, rec port.EmployeeRecord) (*domain.Employee, error) {
	if s == nil || s.employees == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "hris.employee.upsert", "employee repo not configured", nil)
	}
	rec.EmployeeNo = strings.TrimSpace(rec.EmployeeNo)
	if rec.EmployeeNo == "" {
		return nil, derrors.Validation("hris.employee_no_required", "employee_no is required")
	}
	existing, err := s.employees.FindByEmployeeNo(ctx, rec.EmployeeNo)
	if err != nil && !derrors.IsNotFound(err) {
		return nil, err
	}
	var e *domain.Employee
	if existing != nil {
		e = existing
		e.FullName = strings.TrimSpace(rec.FullName)
		e.Email = strings.TrimSpace(rec.Email)
		e.Phone = strings.TrimSpace(rec.Phone)
		e.Department = strings.TrimSpace(rec.Department)
		e.Position = strings.TrimSpace(rec.Position)
		e.ManagerEmployeeNo = strings.TrimSpace(rec.ManagerEmployeeNo)
		e.HireDate = rec.HireDate
		e.ResignDate = rec.ResignDate
		if rec.Status != "" && rec.Status.Valid() {
			e.Status = rec.Status
		}
		e.KYCCompleted = rec.KYCCompleted
		e.NPWP = strings.TrimSpace(rec.NPWP)
		e.BankAccountNo = strings.TrimSpace(rec.BankAccountNo)
		e.BranchID = rec.BranchID
		e.RoleRecommendations = rec.RoleRecommendations
		e.UpdatedAt = time.Now().UTC()
	} else {
		// Fresh row — go through NewEmployee so invariants are enforced.
		fresh, nerr := domain.NewEmployee(rec.EmployeeNo, rec.FullName)
		if nerr != nil {
			return nil, nerr
		}
		e = fresh
		e.Email = strings.TrimSpace(rec.Email)
		e.Phone = strings.TrimSpace(rec.Phone)
		e.Department = strings.TrimSpace(rec.Department)
		e.Position = strings.TrimSpace(rec.Position)
		e.ManagerEmployeeNo = strings.TrimSpace(rec.ManagerEmployeeNo)
		e.HireDate = rec.HireDate
		e.ResignDate = rec.ResignDate
		if rec.Status != "" && rec.Status.Valid() {
			e.Status = rec.Status
		}
		e.KYCCompleted = rec.KYCCompleted
		e.NPWP = strings.TrimSpace(rec.NPWP)
		e.BankAccountNo = strings.TrimSpace(rec.BankAccountNo)
		e.BranchID = rec.BranchID
		e.RoleRecommendations = rec.RoleRecommendations
	}
	if err := s.employees.Upsert(ctx, e); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
		Module:     "hris",
		RecordType: "hris.employee",
		RecordID:   e.ID.String(),
		After:      string(e.Status),
		Reason:     "wave118.employee.upsert",
	})
	return e, nil
}

// Resign transitions an employee to resigned. Idempotent on re-call.
func (s *EmployeeService) Resign(ctx context.Context, employeeNo string, at time.Time, reason string) (*domain.Employee, error) {
	if s == nil || s.employees == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "hris.employee.resign", "employee repo not configured", nil)
	}
	e, err := s.employees.FindByEmployeeNo(ctx, employeeNo)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, derrors.NotFound("hris.employee_not_found", "employee not found")
	}
	if err := e.Resign(at, reason); err != nil {
		return nil, err
	}
	if err := s.employees.Upsert(ctx, e); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
		Module:     "hris",
		RecordType: "hris.employee",
		RecordID:   e.ID.String(),
		FieldChanged: "status",
		After:      string(e.Status),
		Reason:     "wave118.employee.resign",
	})
	return e, nil
}

// Reinstate transitions an employee from Suspended → Active.
func (s *EmployeeService) Reinstate(ctx context.Context, employeeNo string) (*domain.Employee, error) {
	if s == nil || s.employees == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "hris.employee.reinstate", "employee repo not configured", nil)
	}
	e, err := s.employees.FindByEmployeeNo(ctx, employeeNo)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, derrors.NotFound("hris.employee_not_found", "employee not found")
	}
	if err := e.Reinstate(); err != nil {
		return nil, err
	}
	if err := s.employees.Upsert(ctx, e); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
		Module:     "hris",
		RecordType: "hris.employee",
		RecordID:   e.ID.String(),
		FieldChanged: "status",
		After:      string(e.Status),
		Reason:     "wave118.employee.reinstate",
	})
	return e, nil
}

// FindByEmployeeNo is the read path for the HTTP layer.
func (s *EmployeeService) FindByEmployeeNo(ctx context.Context, employeeNo string) (*domain.Employee, error) {
	if s == nil || s.employees == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "hris.employee.find", "employee repo not configured", nil)
	}
	return s.employees.FindByEmployeeNo(ctx, employeeNo)
}

// Search runs a basic projection. limit defaults to 50, capped at 200.
func (s *EmployeeService) Search(ctx context.Context, f port.EmployeeFilter) ([]domain.Employee, int, error) {
	if s == nil || s.employees == nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "hris.employee.search", "employee repo not configured", nil)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	return s.employees.List(ctx, f)
}

// IsResignedBefore satisfies port.HRISResignedReader. It maps a salesUserID
// → employee_no via the identity bridge, then consults the domain rule.
// Because the identity bridge needs to be wired by the svc binary, this
// implementation lives on the SyncService (where the identity reader is
// available) and delegates here for the actual rule check.
//
// Standalone usage (e.g. when callers already know the employee_no) goes
// through EmployeeService.IsResignedByEmployeeNo below.
func (s *EmployeeService) IsResignedByEmployeeNo(ctx context.Context, employeeNo string, t time.Time) bool {
	if s == nil || s.employees == nil {
		return false
	}
	e, err := s.employees.FindByEmployeeNo(ctx, employeeNo)
	if err != nil || e == nil {
		return false
	}
	return e.IsResignedBefore(t)
}
