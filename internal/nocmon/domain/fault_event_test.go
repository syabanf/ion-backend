package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestFaultStateMachineHappyPath exercises the full open → ack →
// investigating → mitigated → resolved chain.
func TestFaultStateMachineHappyPath(t *testing.T) {
	f, err := NewFaultEvent(FaultKindDeviceDown, FaultSeverityHigh, nil, "")
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if f.Status != FaultStatusOpen {
		t.Fatalf("fresh fault should be open, got %s", f.Status)
	}
	by := uuid.New()
	now := time.Now().UTC()
	if err := f.Acknowledge(by, now); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if f.Status != FaultStatusAcknowledged {
		t.Errorf("expected acknowledged, got %s", f.Status)
	}
	if err := f.Investigate(by, now); err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if f.Status != FaultStatusInvestigating {
		t.Errorf("expected investigating, got %s", f.Status)
	}
	if err := f.Mitigate(by, now, "fixed the fiber splice"); err != nil {
		t.Fatalf("mitigate: %v", err)
	}
	if f.Status != FaultStatusMitigated {
		t.Errorf("expected mitigated, got %s", f.Status)
	}
	if f.RootCause != "fixed the fiber splice" {
		t.Errorf("expected root_cause persisted, got %q", f.RootCause)
	}
	if err := f.Resolve(by, now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if f.Status != FaultStatusResolved {
		t.Errorf("expected resolved, got %s", f.Status)
	}
}

func TestFaultInvestigateShortcutFromOpen(t *testing.T) {
	// ConvertFaultToWO jumps open → investigating; verify the
	// shortcut stamps acknowledged_at + acknowledged_by anyway so
	// the audit trail shows who took ownership.
	f, _ := NewFaultEvent(FaultKindDeviceDown, FaultSeverityHigh, nil, "")
	by := uuid.New()
	now := time.Now().UTC()
	if err := f.Investigate(by, now); err != nil {
		t.Fatalf("investigate from open: %v", err)
	}
	if f.Status != FaultStatusInvestigating {
		t.Errorf("expected investigating, got %s", f.Status)
	}
	if f.AcknowledgedAt == nil || f.AcknowledgedBy == nil {
		t.Errorf("acknowledged_at + by should be stamped on investigate shortcut")
	}
}

func TestFaultStateMachineRefusesIllegalMoves(t *testing.T) {
	f, _ := NewFaultEvent(FaultKindDeviceDown, FaultSeverityHigh, nil, "")
	by := uuid.New()
	now := time.Now().UTC()
	// Mitigate from open should fail.
	if err := f.Mitigate(by, now, ""); err == nil {
		t.Errorf("Mitigate from open should be refused")
	}
	// Resolve from open should fail — the SM forces ack/invest first.
	if err := f.Resolve(by, now); err == nil {
		t.Errorf("Resolve from open should be refused")
	}
	// Drive through ack → resolve (allowed early close-out from
	// acknowledged).
	if err := f.Acknowledge(by, now); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if err := f.Resolve(by, now); err != nil {
		t.Errorf("Resolve from acknowledged should be permitted: %v", err)
	}
	// Resolve again — idempotent.
	if err := f.Resolve(by, now); err != nil {
		t.Errorf("Resolve on already-resolved should be idempotent: %v", err)
	}
	// Acknowledge a resolved fault — refused.
	if err := f.Acknowledge(by, now); err == nil {
		t.Errorf("Acknowledge of resolved fault should be refused")
	}
}

func TestFaultMarkDuplicate(t *testing.T) {
	f, _ := NewFaultEvent(FaultKindDeviceDown, FaultSeverityHigh, nil, "")
	other := uuid.New()
	if err := f.MarkDuplicate(other, time.Now().UTC()); err != nil {
		t.Fatalf("MarkDuplicate: %v", err)
	}
	if f.Status != FaultStatusDuplicate {
		t.Errorf("expected duplicate, got %s", f.Status)
	}
	// Acknowledge a duplicate — refused (terminal).
	if err := f.Acknowledge(uuid.New(), time.Now().UTC()); err == nil {
		t.Errorf("Acknowledge of duplicate fault should be refused")
	}
}

func TestNewFaultEventValidatesInputs(t *testing.T) {
	if _, err := NewFaultEvent(FaultKind("bogus"), FaultSeverityHigh, nil, ""); err == nil {
		t.Errorf("invalid kind should fail")
	}
	if _, err := NewFaultEvent(FaultKindDeviceDown, FaultSeverity("bogus"), nil, ""); err == nil {
		t.Errorf("invalid severity should fail")
	}
}
