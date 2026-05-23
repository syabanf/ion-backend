package domain

import (
	"testing"
	"time"
)

func TestNewEmployeeEvent_Valid(t *testing.T) {
	ev, err := NewEmployeeEvent("EMP1", EventKindResigned, map[string]any{"reason": "x"},
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "manual")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Source != "manual" {
		t.Fatalf("source: want manual, got %s", ev.Source)
	}
	if ev.Processed {
		t.Fatal("processed should default false")
	}
}

func TestNewEmployeeEvent_Invalid(t *testing.T) {
	if _, err := NewEmployeeEvent("", EventKindResigned, nil, time.Now(), ""); err == nil {
		t.Fatal("expected error for empty employee_no")
	}
	if _, err := NewEmployeeEvent("EMP1", "weird_kind", nil, time.Now(), ""); err == nil {
		t.Fatal("expected error for invalid kind")
	}
	if _, err := NewEmployeeEvent("EMP1", EventKindResigned, nil, time.Time{}, ""); err == nil {
		t.Fatal("expected error for zero occurred_at")
	}
}

func TestEmployeeEvent_ProcessHook_Resign(t *testing.T) {
	ev, _ := NewEmployeeEvent("EMP1", EventKindResigned, nil, time.Now().UTC(), "stub")
	d := ev.ProcessHook()
	if !d.CancelCommissions {
		t.Fatal("resigned event should cancel commissions")
	}
	if !d.DeactivateUser {
		t.Fatal("resigned event should deactivate user")
	}
	if d.AuditReason == "" {
		t.Fatal("audit reason should be set")
	}
}

func TestEmployeeEvent_ProcessHook_Transfer(t *testing.T) {
	ev, _ := NewEmployeeEvent("EMP1", EventKindTransferred, nil, time.Now().UTC(), "stub")
	d := ev.ProcessHook()
	if !d.ReassignFieldQueue {
		t.Fatal("transferred event should reassign field queue")
	}
	if d.CancelCommissions {
		t.Fatal("transferred event should NOT cancel commissions")
	}
}

func TestEmployeeEvent_ProcessHook_RoleChanged(t *testing.T) {
	ev, _ := NewEmployeeEvent("EMP1", EventKindRoleChanged, nil, time.Now().UTC(), "stub")
	d := ev.ProcessHook()
	if !d.UpdateRBAC {
		t.Fatal("role_changed event should update RBAC")
	}
}

func TestEmployeeEvent_ProcessHook_AuditOnly(t *testing.T) {
	ev, _ := NewEmployeeEvent("EMP1", EventKindHired, nil, time.Now().UTC(), "stub")
	d := ev.ProcessHook()
	if !d.AuditOnly {
		t.Fatal("hired event should be audit-only")
	}
	if d.CancelCommissions || d.DeactivateUser || d.ReassignFieldQueue || d.UpdateRBAC {
		t.Fatalf("hired event should fire no bridges, got %+v", d)
	}
}
