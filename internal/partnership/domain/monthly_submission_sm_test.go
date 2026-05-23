package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — MonthlySubmission state-machine contract tests
//
//	draft → submitted → confirmed (settlement issued)
//	                  ↘ returned → draft (re-submit via MarkDraft)
//	draft|returned → cancelled (terminal)
// =====================================================================

func float64Ptr(f float64) *float64 { return &f }
func intPtr(i int) *int             { return &i }

// fillSubmissionForSubmit populates the fields Submit() requires so the
// state-machine can advance.
func fillSubmissionForSubmit(s *MonthlySubmission) {
	s.GrossRevenue = float64Ptr(10000)
	s.NetRevenue = float64Ptr(8000)
	s.SubscriberCount = intPtr(50)
	s.EvidenceURL = "https://files.ion/evidence.pdf"
	s.EvidenceHash = "deadbeef"
}

func newMonthlySubmissionAt(t *testing.T, status SubmissionStatus) *MonthlySubmission {
	t.Helper()
	s, err := NewMonthlySubmission(uuid.New(), uuid.New(), 2026, 5)
	if err != nil {
		t.Fatalf("NewMonthlySubmission: %v", err)
	}
	switch status {
	case SubmissionStatusDraft:
	case SubmissionStatusSubmitted:
		fillSubmissionForSubmit(s)
		if err := s.Submit(uuid.New()); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	case SubmissionStatusConfirmed:
		fillSubmissionForSubmit(s)
		if err := s.Submit(uuid.New()); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := s.Confirm(uuid.New()); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
	case SubmissionStatusReturned:
		fillSubmissionForSubmit(s)
		if err := s.Submit(uuid.New()); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := s.Return("missing evidence", time.Now()); err != nil {
			t.Fatalf("Return: %v", err)
		}
	case SubmissionStatusCancelled:
		if err := s.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}
	return s
}

func TestMonthlySubmissionSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		name       string
		from       SubmissionStatus
		action     func(*MonthlySubmission) error
		wantStatus SubmissionStatus
	}{
		{"draft -> submitted", SubmissionStatusDraft, func(s *MonthlySubmission) error {
			fillSubmissionForSubmit(s)
			return s.Submit(uuid.New())
		}, SubmissionStatusSubmitted},
		{"submitted -> confirmed", SubmissionStatusSubmitted, func(s *MonthlySubmission) error { return s.Confirm(uuid.New()) }, SubmissionStatusConfirmed},
		{"submitted -> returned", SubmissionStatusSubmitted, func(s *MonthlySubmission) error { return s.Return("fix figures", time.Now()) }, SubmissionStatusReturned},
		{"returned -> draft (MarkDraft)", SubmissionStatusReturned, func(s *MonthlySubmission) error { return s.MarkDraft() }, SubmissionStatusDraft},
		{"returned -> submitted (re-submit)", SubmissionStatusReturned, func(s *MonthlySubmission) error {
			fillSubmissionForSubmit(s)
			return s.Submit(uuid.New())
		}, SubmissionStatusSubmitted},
		{"draft -> cancelled", SubmissionStatusDraft, func(s *MonthlySubmission) error { return s.Cancel() }, SubmissionStatusCancelled},
		{"returned -> cancelled", SubmissionStatusReturned, func(s *MonthlySubmission) error { return s.Cancel() }, SubmissionStatusCancelled},
		{"cancelled -> cancelled (idempotent)", SubmissionStatusCancelled, func(s *MonthlySubmission) error { return s.Cancel() }, SubmissionStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMonthlySubmissionAt(t, tc.from)
			if err := tc.action(s); err != nil {
				t.Fatalf("action: %v", err)
			}
			if s.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", s.Status, tc.wantStatus)
			}
		})
	}
}

func TestMonthlySubmissionSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     SubmissionStatus
		action   func(*MonthlySubmission) error
		wantCode string
	}{
		{"draft -> confirmed (skip submitted)", SubmissionStatusDraft, func(s *MonthlySubmission) error { return s.Confirm(uuid.New()) }, "submission.cannot_confirm"},
		{"draft -> returned (no submission)", SubmissionStatusDraft, func(s *MonthlySubmission) error { return s.Return("n/a", time.Now()) }, "submission.cannot_return"},
		{"draft -> draft (MarkDraft no-op)", SubmissionStatusDraft, func(s *MonthlySubmission) error { return s.MarkDraft() }, "submission.cannot_redraft"},
		{"submitted -> submitted (re-submit)", SubmissionStatusSubmitted, func(s *MonthlySubmission) error { return s.Submit(uuid.New()) }, "submission.cannot_submit"},
		{"submitted -> draft (MarkDraft only from returned)", SubmissionStatusSubmitted, func(s *MonthlySubmission) error { return s.MarkDraft() }, "submission.cannot_redraft"},
		{"confirmed -> returned (backward)", SubmissionStatusConfirmed, func(s *MonthlySubmission) error { return s.Return("n/a", time.Now()) }, "submission.cannot_return"},
		{"confirmed -> cancelled", SubmissionStatusConfirmed, func(s *MonthlySubmission) error { return s.Cancel() }, "submission.cannot_cancel"},
		{"cancelled -> submitted", SubmissionStatusCancelled, func(s *MonthlySubmission) error { return s.Submit(uuid.New()) }, "submission.cannot_submit"},
		// Validation errors on Submit
		{"submit missing gross revenue", SubmissionStatusDraft, func(s *MonthlySubmission) error {
			// don't populate
			return s.Submit(uuid.New())
		}, "submission.gross_revenue_required"},
		{"return without reason", SubmissionStatusSubmitted, func(s *MonthlySubmission) error { return s.Return("", time.Now()) }, "submission.return_reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMonthlySubmissionAt(t, tc.from)
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
