package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 113 — DeviceSwap state-machine tests.
//
// Lifecycle:
//
//	requested → approved → staged → technician_assigned → swapped → closed
//	                                                              ↘ rolled_back
// =====================================================================

func newSwapAt(t *testing.T, status SwapStatus) *DeviceSwap {
	t.Helper()
	customer := uuid.New()
	faulty := uuid.New()
	replacement := uuid.New()
	tech := uuid.New()
	approver := uuid.New()
	at := time.Now().UTC()
	s, err := NewDeviceSwap(customer, faulty, "broken ONT", nil, nil)
	if err != nil {
		t.Fatalf("NewDeviceSwap: %v", err)
	}
	switch status {
	case SwapStatusRequested:
	case SwapStatusApproved:
		if err := s.Approve(approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
	case SwapStatusStaged:
		if err := s.Approve(approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Stage(replacement); err != nil {
			t.Fatalf("Stage: %v", err)
		}
	case SwapStatusTechnicianAssigned:
		if err := s.Approve(approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Stage(replacement); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := s.AssignTechnician(tech, nil); err != nil {
			t.Fatalf("AssignTechnician: %v", err)
		}
	case SwapStatusSwapped:
		if err := s.Approve(approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Stage(replacement); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := s.AssignTechnician(tech, nil); err != nil {
			t.Fatalf("AssignTechnician: %v", err)
		}
		if err := s.Complete(nil, at); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	case SwapStatusClosed:
		if err := s.Approve(approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Stage(replacement); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := s.AssignTechnician(tech, nil); err != nil {
			t.Fatalf("AssignTechnician: %v", err)
		}
		if err := s.Complete(nil, at); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	case SwapStatusRolledBack:
		if err := s.Approve(approver); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Stage(replacement); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := s.AssignTechnician(tech, nil); err != nil {
			t.Fatalf("AssignTechnician: %v", err)
		}
		if err := s.Complete(nil, at); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := s.RollBack("ONT won't sync"); err != nil {
			t.Fatalf("RollBack: %v", err)
		}
	}
	return s
}

func TestSwapSM_ValidTransitions(t *testing.T) {
	approver := uuid.New()
	replacement := uuid.New()
	tech := uuid.New()
	at := time.Now().UTC()

	cases := []struct {
		name       string
		from       SwapStatus
		action     func(*DeviceSwap) error
		wantStatus SwapStatus
	}{
		{"requested → approved", SwapStatusRequested,
			func(s *DeviceSwap) error { return s.Approve(approver) }, SwapStatusApproved},
		{"approved → staged", SwapStatusApproved,
			func(s *DeviceSwap) error { return s.Stage(replacement) }, SwapStatusStaged},
		{"staged → technician_assigned", SwapStatusStaged,
			func(s *DeviceSwap) error { return s.AssignTechnician(tech, nil) }, SwapStatusTechnicianAssigned},
		{"technician_assigned → swapped", SwapStatusTechnicianAssigned,
			func(s *DeviceSwap) error { return s.Complete(nil, at) }, SwapStatusSwapped},
		{"swapped → closed", SwapStatusSwapped,
			func(s *DeviceSwap) error { return s.Close() }, SwapStatusClosed},
		{"swapped → rolled_back", SwapStatusSwapped,
			func(s *DeviceSwap) error { return s.RollBack("won't sync") }, SwapStatusRolledBack},
		// Idempotence
		{"approved → approved", SwapStatusApproved,
			func(s *DeviceSwap) error { return s.Approve(approver) }, SwapStatusApproved},
		{"staged → staged (same replacement)", SwapStatusStaged,
			func(s *DeviceSwap) error { return s.Stage(*s.ReplacementDeviceID) }, SwapStatusStaged},
		{"closed → closed", SwapStatusClosed,
			func(s *DeviceSwap) error { return s.Close() }, SwapStatusClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSwapAt(t, tc.from)
			if err := tc.action(s); err != nil {
				t.Fatalf("action: %v", err)
			}
			if s.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", s.Status, tc.wantStatus)
			}
		})
	}
}

func TestSwapSM_InvalidTransitions(t *testing.T) {
	replacement := uuid.New()
	otherReplacement := uuid.New()
	tech := uuid.New()
	at := time.Now().UTC()

	cases := []struct {
		name     string
		from     SwapStatus
		action   func(*DeviceSwap) error
		wantCode string
	}{
		{"requested → staged (skip approve)", SwapStatusRequested,
			func(s *DeviceSwap) error { return s.Stage(replacement) },
			"swap.invalid_state_transition"},
		{"approved → assign (skip stage)", SwapStatusApproved,
			func(s *DeviceSwap) error { return s.AssignTechnician(tech, nil) },
			"swap.invalid_state_transition"},
		{"closed → rollback", SwapStatusClosed,
			func(s *DeviceSwap) error { return s.RollBack("reason") },
			"swap.invalid_state_transition"},
		{"approved → rollback (only from swapped)", SwapStatusApproved,
			func(s *DeviceSwap) error { return s.RollBack("reason") },
			"swap.invalid_state_transition"},
		// Validation
		{"stage with nil replacement", SwapStatusApproved,
			func(s *DeviceSwap) error { return s.Stage(uuid.Nil) },
			"swap.replacement_required"},
		{"stage with different replacement after staged", SwapStatusStaged,
			func(s *DeviceSwap) error { return s.Stage(otherReplacement) },
			"swap.replacement_already_set"},
		{"complete without technician_assigned", SwapStatusStaged,
			func(s *DeviceSwap) error { return s.Complete(nil, at) },
			"swap.invalid_state_transition"},
		{"rollback without reason", SwapStatusSwapped,
			func(s *DeviceSwap) error { return s.RollBack("  ") },
			"swap.reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSwapAt(t, tc.from)
			err := tc.action(s)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", s.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("err type: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", de.Code, tc.wantCode)
			}
		})
	}
}

func TestNewDeviceSwap_Validation(t *testing.T) {
	if _, err := NewDeviceSwap(uuid.Nil, uuid.New(), "x", nil, nil); err == nil {
		t.Fatalf("nil customer should error")
	}
	if _, err := NewDeviceSwap(uuid.New(), uuid.Nil, "x", nil, nil); err == nil {
		t.Fatalf("nil faulty should error")
	}
	if _, err := NewDeviceSwap(uuid.New(), uuid.New(), "  ", nil, nil); err == nil {
		t.Fatalf("empty reason should error")
	}
}
