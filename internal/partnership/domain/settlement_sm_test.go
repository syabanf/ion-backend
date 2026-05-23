package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — Settlement state-machine contract tests (TC-SM-STL-*)
//
//	pending → approved → paid (terminal)
//	       ↘ cancelled        (also from approved)
// =====================================================================

func newSettlementAt(t *testing.T, status SettlementStatus) *Settlement {
	t.Helper()
	s, err := NewSettlement(uuid.New(), uuid.New(), 2026, 5, 10000, 8000, 2400, 264, 2136, nil)
	if err != nil {
		t.Fatalf("NewSettlement: %v", err)
	}
	now := time.Now()
	switch status {
	case SettlementStatusPending:
	case SettlementStatusApproved:
		if err := s.Approve(uuid.New()); err != nil {
			t.Fatalf("Approve: %v", err)
		}
	case SettlementStatusPaid:
		if err := s.Approve(uuid.New()); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Pay(now); err != nil {
			t.Fatalf("Pay: %v", err)
		}
	case SettlementStatusCancelled:
		if err := s.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}
	return s
}

func TestSettlementSM_ValidTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		from       SettlementStatus
		action     func(*Settlement) error
		wantStatus SettlementStatus
	}{
		{"pending -> approved", SettlementStatusPending, func(s *Settlement) error { return s.Approve(uuid.New()) }, SettlementStatusApproved},
		{"approved -> paid", SettlementStatusApproved, func(s *Settlement) error { return s.Pay(now) }, SettlementStatusPaid},
		{"pending -> cancelled", SettlementStatusPending, func(s *Settlement) error { return s.Cancel() }, SettlementStatusCancelled},
		{"approved -> cancelled", SettlementStatusApproved, func(s *Settlement) error { return s.Cancel() }, SettlementStatusCancelled},
		// Idempotent destinations
		{"approved -> approved (idempotent)", SettlementStatusApproved, func(s *Settlement) error { return s.Approve(uuid.New()) }, SettlementStatusApproved},
		{"paid -> paid (idempotent)", SettlementStatusPaid, func(s *Settlement) error { return s.Pay(now) }, SettlementStatusPaid},
		{"cancelled -> cancelled (idempotent)", SettlementStatusCancelled, func(s *Settlement) error { return s.Cancel() }, SettlementStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSettlementAt(t, tc.from)
			if err := tc.action(s); err != nil {
				t.Fatalf("action: %v", err)
			}
			if s.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", s.Status, tc.wantStatus)
			}
		})
	}
}

func TestSettlementSM_InvalidTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		from     SettlementStatus
		action   func(*Settlement) error
		wantCode string
	}{
		{"pending -> paid (skip approved)", SettlementStatusPending, func(s *Settlement) error { return s.Pay(now) }, "settlement.cannot_pay"},
		{"paid -> approved (backward)", SettlementStatusPaid, func(s *Settlement) error { return s.Approve(uuid.New()) }, "settlement.cannot_approve"},
		{"paid -> cancelled", SettlementStatusPaid, func(s *Settlement) error { return s.Cancel() }, "settlement.cannot_cancel_paid"},
		{"cancelled -> approved", SettlementStatusCancelled, func(s *Settlement) error { return s.Approve(uuid.New()) }, "settlement.cannot_approve"},
		{"cancelled -> paid", SettlementStatusCancelled, func(s *Settlement) error { return s.Pay(now) }, "settlement.cannot_pay"},
		// Validation
		{"approve with nil byUserID", SettlementStatusPending, func(s *Settlement) error { return s.Approve(uuid.Nil) }, "settlement.approved_by_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSettlementAt(t, tc.from)
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
