package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — EWO state-machine contract tests (TC-SM-EWO-*)
//
//	pending     → in_progress  (Start; flips ScheduleLocked=true)
//	in_progress → completed    (Complete)
//	pending|in_progress → cancelled (Cancel with reason)
//
// Wave 96 — once ScheduleLocked=true, Reschedule rejects (TC-TL-009).
// =====================================================================

func newEWOAt(t *testing.T, status EWOStatus) *EWO {
	t.Helper()
	e, err := NewEWO(uuid.New(), uuid.New(), uuid.New(), "EWO-1", "test")
	if err != nil {
		t.Fatalf("NewEWO: %v", err)
	}
	switch status {
	case EWOStatusPending:
	case EWOStatusInProgress:
		if err := e.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
	case EWOStatusCompleted:
		if err := e.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := e.Complete(); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	case EWOStatusCancelled:
		if err := e.Cancel("ops cancel"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}
	return e
}

func TestEWOSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		name       string
		from       EWOStatus
		action     func(*EWO) error
		wantStatus EWOStatus
	}{
		{"pending -> in_progress (Start)", EWOStatusPending, func(e *EWO) error { return e.Start() }, EWOStatusInProgress},
		{"in_progress -> completed", EWOStatusInProgress, func(e *EWO) error { return e.Complete() }, EWOStatusCompleted},
		{"pending -> cancelled", EWOStatusPending, func(e *EWO) error { return e.Cancel("budget cut") }, EWOStatusCancelled},
		{"in_progress -> cancelled", EWOStatusInProgress, func(e *EWO) error { return e.Cancel("ops halt") }, EWOStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEWOAt(t, tc.from)
			if err := tc.action(e); err != nil {
				t.Fatalf("action: %v", err)
			}
			if e.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", e.Status, tc.wantStatus)
			}
		})
	}
}

func TestEWOSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     EWOStatus
		action   func(*EWO) error
		wantCode string
	}{
		{"pending -> completed (skip in_progress)", EWOStatusPending, func(e *EWO) error { return e.Complete() }, "ewo.invalid_transition"},
		{"in_progress -> in_progress (re-Start)", EWOStatusInProgress, func(e *EWO) error { return e.Start() }, "ewo.invalid_transition"},
		{"completed -> in_progress (backward Start)", EWOStatusCompleted, func(e *EWO) error { return e.Start() }, "ewo.invalid_transition"},
		{"completed -> completed (double)", EWOStatusCompleted, func(e *EWO) error { return e.Complete() }, "ewo.invalid_transition"},
		{"cancelled -> in_progress", EWOStatusCancelled, func(e *EWO) error { return e.Start() }, "ewo.invalid_transition"},
		// Cancel rules
		{"completed -> cancelled", EWOStatusCompleted, func(e *EWO) error { return e.Cancel("oops") }, "ewo.already_completed"},
		{"cancelled -> cancelled", EWOStatusCancelled, func(e *EWO) error { return e.Cancel("again") }, "ewo.already_cancelled"},
		// Reason validation
		{"cancel without reason", EWOStatusPending, func(e *EWO) error { return e.Cancel("") }, "ewo.cancel_reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEWOAt(t, tc.from)
			err := tc.action(e)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", e.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("error is not *derrors.Error: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", de.Code, tc.wantCode)
			}
		})
	}
}

// TestEWOSM_ScheduleLockOnStart — Wave 96 acceptance criterion. Once
// Start flips status to in_progress, ScheduleLocked=true and any
// subsequent Reschedule attempt must fail with `ewo.schedule_locked`.
func TestEWOSM_ScheduleLockOnStart(t *testing.T) {
	e, err := NewEWO(uuid.New(), uuid.New(), uuid.New(), "EWO-1", "test")
	if err != nil {
		t.Fatalf("NewEWO: %v", err)
	}
	if e.ScheduleLocked {
		t.Fatal("freshly-created EWO must not be ScheduleLocked")
	}
	// Schedule first so Reschedule has a previous window to mutate.
	start := time.Now()
	end := start.Add(48 * time.Hour)
	if err := e.Schedule(start, end, uuid.New(), nil); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !e.ScheduleLocked {
		t.Fatal("after Start, ScheduleLocked must be true")
	}
	_, err = e.Reschedule(start.Add(1*time.Hour), end.Add(1*time.Hour), uuid.New(), "shift")
	if err == nil {
		t.Fatal("Reschedule on a started EWO should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("error not *derrors.Error: %T %v", err, err)
	}
	if de.Code != "ewo.schedule_locked" {
		t.Errorf("code = %q, want ewo.schedule_locked", de.Code)
	}
}
