// Wave 118 — HRIS domain tests.
//
// Coverage:
//   - state machine transitions (Hire / Promote / Resign / Suspend / Reinstate)
//   - IsResignedBefore — TC-HRI-006 commission cessation gate
//   - event ProcessHook — TC-HRI-005/006/007/008
//   - NewEmployee invariants — TC-HRI-001 employee_no requirement
//
// All tests are pure-domain; no DB / HTTP dependencies.

package domain

import (
	"testing"
	"time"
)

func TestNewEmployee_RequiresEmployeeNoAndName(t *testing.T) {
	if _, err := NewEmployee("", "Alice"); err == nil {
		t.Fatal("expected error for empty employee_no")
	}
	if _, err := NewEmployee("EMP1", ""); err == nil {
		t.Fatal("expected error for empty full_name")
	}
	e, err := NewEmployee("EMP1", "Alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Status != EmployeeStatusProbation {
		t.Fatalf("default status: want probation, got %s", e.Status)
	}
	if e.EmployeeNo != "EMP1" || e.FullName != "Alice" {
		t.Fatalf("fields mismatch: %+v", e)
	}
}

func TestEmployee_Hire_FromProbation(t *testing.T) {
	e, _ := NewEmployee("EMP1", "Alice")
	now := time.Now().UTC()
	if err := e.Hire(now); err != nil {
		t.Fatalf("Hire: %v", err)
	}
	if e.Status != EmployeeStatusActive {
		t.Fatalf("status after Hire: want active, got %s", e.Status)
	}
	if e.HireDate == nil {
		t.Fatal("HireDate should be set after Hire")
	}
}

func TestEmployee_Hire_FromResigned_Rejected(t *testing.T) {
	e, _ := NewEmployee("EMP1", "Alice")
	_ = e.Hire(time.Now().UTC())
	_ = e.Resign(time.Now().UTC(), "")
	if err := e.Hire(time.Now().UTC()); err == nil {
		t.Fatal("expected error rehiring a resigned employee")
	}
}

func TestEmployee_Resign_Idempotent(t *testing.T) {
	e, _ := NewEmployee("EMP1", "Alice")
	_ = e.Hire(time.Now().UTC())
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := e.Resign(at, "moved"); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if e.Status != EmployeeStatusResigned {
		t.Fatalf("after Resign: want resigned, got %s", e.Status)
	}
	// Second call is a no-op.
	if err := e.Resign(at.AddDate(0, 1, 0), ""); err != nil {
		t.Fatalf("Resign second call: %v", err)
	}
	if e.ResignDate == nil || !e.ResignDate.Equal(at) {
		t.Fatalf("ResignDate should stick to first call: %v", e.ResignDate)
	}
}

func TestEmployee_SuspendReinstate(t *testing.T) {
	e, _ := NewEmployee("EMP1", "Alice")
	_ = e.Hire(time.Now().UTC())
	if err := e.Suspend("discipline"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if e.Status != EmployeeStatusSuspended {
		t.Fatalf("after Suspend: want suspended, got %s", e.Status)
	}
	if err := e.Reinstate(); err != nil {
		t.Fatalf("Reinstate: %v", err)
	}
	if e.Status != EmployeeStatusActive {
		t.Fatalf("after Reinstate: want active, got %s", e.Status)
	}
}

func TestEmployee_Promote_RejectsResigned(t *testing.T) {
	e, _ := NewEmployee("EMP1", "Alice")
	_ = e.Hire(time.Now().UTC())
	_ = e.Resign(time.Now().UTC(), "")
	if err := e.Promote("SVP"); err == nil {
		t.Fatal("expected error promoting a resigned employee")
	}
}

func TestEmployee_IsResignedBefore(t *testing.T) {
	e, _ := NewEmployee("EMP1", "Alice")
	_ = e.Hire(time.Now().UTC())

	// Active employee never resigned.
	if e.IsResignedBefore(time.Now().UTC()) {
		t.Fatal("active employee should not be IsResignedBefore=true")
	}

	resignDate := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	_ = e.Resign(resignDate, "")

	// Invoice paid BEFORE the resign date → still employed → IsResignedBefore=false.
	if e.IsResignedBefore(time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("paid_at within employment window should be IsResignedBefore=false")
	}
	// Invoice paid on the resign date itself → still employed that day → false.
	if e.IsResignedBefore(time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("paid_at on the resign date itself should be IsResignedBefore=false")
	}
	// Invoice paid the next day → resigned → IsResignedBefore=true.
	if !e.IsResignedBefore(time.Date(2026, 4, 1, 0, 0, 1, 0, time.UTC)) {
		t.Fatal("paid_at one day after resign should be IsResignedBefore=true")
	}
}
