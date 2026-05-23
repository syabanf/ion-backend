package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 107 — InputSubmission state-machine contract tests
// (TC-SM-VSUB-*).
//
//	submitted → accepted (terminal positive)
//	submitted → rejected (terminal negative; reason required)
//	submitted → withdrawn (terminal admin)
// =====================================================================

func newInputSubmissionAt(t *testing.T, status SubmissionStatus) *InputSubmission {
	t.Helper()
	cost := 1000.0
	s, err := NewInputSubmission(uuid.New(), uuid.New(), nil, &cost, "ok", nil)
	if err != nil {
		t.Fatalf("NewInputSubmission: %v", err)
	}
	switch status {
	case SubmissionStatusSubmitted:
		// default
	case SubmissionStatusAccepted:
		if err := s.Accept(uuid.New()); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	case SubmissionStatusRejected:
		if err := s.Reject(uuid.New(), "too expensive"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
	case SubmissionStatusWithdrawn:
		if err := s.Withdraw(); err != nil {
			t.Fatalf("Withdraw: %v", err)
		}
	}
	return s
}

// TestVSUB_SubmitValidatesUnitCost — TC-SM-VSUB-001.
func TestVSUB_SubmitValidatesUnitCost(t *testing.T) {
	zero := 0.0
	_, err := NewInputSubmission(uuid.New(), uuid.New(), nil, &zero, "", nil)
	if err == nil {
		t.Fatal("zero unit_cost should be rejected")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "submission.unit_cost_invalid" {
		t.Errorf("err = %v, want submission.unit_cost_invalid", err)
	}
}

// TestVSUB_AcceptHappyPath — TC-SM-VSUB-002.
func TestVSUB_AcceptHappyPath(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusSubmitted)
	reviewer := uuid.New()
	if err := s.Accept(reviewer); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if s.Status != SubmissionStatusAccepted {
		t.Errorf("status = %q, want accepted", s.Status)
	}
	if s.ReviewedBy == nil || *s.ReviewedBy != reviewer {
		t.Errorf("reviewed_by not snapshotted")
	}
	if s.ReviewedAt == nil {
		t.Error("reviewed_at not set")
	}
}

// TestVSUB_RejectRequiresReason — TC-SM-VSUB-003.
func TestVSUB_RejectRequiresReason(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusSubmitted)
	err := s.Reject(uuid.New(), "")
	if err == nil {
		t.Fatal("Reject with no reason should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T", err)
	}
	if de.Code != "submission.reject_reason_required" {
		t.Errorf("code = %q, want submission.reject_reason_required", de.Code)
	}
}

// TestVSUB_WithdrawHappyPath — TC-SM-VSUB-004.
func TestVSUB_WithdrawHappyPath(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusSubmitted)
	if err := s.Withdraw(); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if s.Status != SubmissionStatusWithdrawn {
		t.Errorf("status = %q, want withdrawn", s.Status)
	}
}

// TestVSUB_AcceptAfterRejectBlocked — TC-SM-VSUB-005.
func TestVSUB_AcceptAfterRejectBlocked(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusRejected)
	err := s.Accept(uuid.New())
	if err == nil {
		t.Fatal("Accept after reject should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "submission.cannot_accept" {
		t.Errorf("err = %v, want submission.cannot_accept", err)
	}
}

// TestVSUB_AcceptAfterWithdrawBlocked — TC-SM-VSUB-006.
func TestVSUB_AcceptAfterWithdrawBlocked(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusWithdrawn)
	err := s.Accept(uuid.New())
	if err == nil {
		t.Fatal("Accept after withdraw should fail")
	}
}

// TestVSUB_WithdrawAfterAcceptBlocked — TC-SM-VSUB-007.
func TestVSUB_WithdrawAfterAcceptBlocked(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusAccepted)
	err := s.Withdraw()
	if err == nil {
		t.Fatal("Withdraw after accept should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "submission.cannot_withdraw" {
		t.Errorf("err = %v, want submission.cannot_withdraw", err)
	}
}

// TestVSUB_AcceptIdempotent — TC-SM-VSUB-008.
func TestVSUB_AcceptIdempotent(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusAccepted)
	if err := s.Accept(uuid.New()); err != nil {
		t.Errorf("Accept idempotent should pass: %v", err)
	}
}

// TestVSUB_WithdrawIdempotent — TC-SM-VSUB-009.
func TestVSUB_WithdrawIdempotent(t *testing.T) {
	s := newInputSubmissionAt(t, SubmissionStatusWithdrawn)
	if err := s.Withdraw(); err != nil {
		t.Errorf("Withdraw idempotent should pass: %v", err)
	}
}

// TestVSUB_IsTerminalContract — TC-SM-VSUB-010.
func TestVSUB_IsTerminalContract(t *testing.T) {
	cases := []struct {
		status SubmissionStatus
		want   bool
	}{
		{SubmissionStatusSubmitted, false},
		{SubmissionStatusAccepted, true},
		{SubmissionStatusRejected, true},
		{SubmissionStatusWithdrawn, true},
	}
	for _, tc := range cases {
		s := newInputSubmissionAt(t, tc.status)
		if got := s.IsTerminal(); got != tc.want {
			t.Errorf("status=%q IsTerminal()=%v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestVSUB_ComputeOnTimePct_Boundaries — quick guard on the helper.
func TestVSUB_ComputeOnTimePct_Boundaries(t *testing.T) {
	cases := []struct {
		onTime int
		total  int
		want   float64
	}{
		{0, 0, 0},
		{0, 10, 0},
		{5, 10, 50},
		{10, 10, 100},
		// clamp guards
		{20, 10, 100},
		{-5, 10, 0},
	}
	for _, tc := range cases {
		got := ComputeOnTimePct(tc.onTime, tc.total)
		if got != tc.want {
			t.Errorf("ComputeOnTimePct(%d,%d) = %v, want %v", tc.onTime, tc.total, got, tc.want)
		}
	}
}
