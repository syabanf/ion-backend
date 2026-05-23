package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 113 — FirmwareUpgradeJob state-machine tests.
//
// Lifecycle:
//
//	scheduled → staged → in_progress → succeeded
//	                                 ↘ failed → rolled_back (after max_retries)
//	scheduled|staged → cancelled
// =====================================================================

func newJobAt(t *testing.T, status UpgradeJobStatus) *FirmwareUpgradeJob {
	t.Helper()
	j, err := NewFirmwareUpgradeJob(uuid.New(), nil, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("NewFirmwareUpgradeJob: %v", err)
	}
	// Make retries easy to exhaust in tests.
	j.MaxRetries = 2
	at := time.Now().UTC()
	switch status {
	case UpgradeJobStatusScheduled:
	case UpgradeJobStatusStaged:
		if err := j.Stage(); err != nil {
			t.Fatalf("Stage: %v", err)
		}
	case UpgradeJobStatusInProgress:
		if err := j.Stage(); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := j.Start("v1", at); err != nil {
			t.Fatalf("Start: %v", err)
		}
	case UpgradeJobStatusSucceeded:
		if err := j.Stage(); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := j.Start("v1", at); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := j.Succeed(at); err != nil {
			t.Fatalf("Succeed: %v", err)
		}
	case UpgradeJobStatusFailed:
		if err := j.Stage(); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := j.Start("v1", at); err != nil {
			t.Fatalf("Start: %v", err)
		}
		// Exhaust the retry budget.
		for {
			retryable, err := j.Fail("boom")
			if err != nil {
				t.Fatalf("Fail: %v", err)
			}
			if !retryable {
				break
			}
			// retryable=true brings us back to scheduled; re-stage + start
			if err := j.Stage(); err != nil {
				t.Fatalf("Stage retry: %v", err)
			}
			if err := j.Start("v1", at); err != nil {
				t.Fatalf("Start retry: %v", err)
			}
		}
	case UpgradeJobStatusRolledBack:
		if err := j.Stage(); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := j.Start("v1", at); err != nil {
			t.Fatalf("Start: %v", err)
		}
		for {
			retryable, err := j.Fail("boom")
			if err != nil {
				t.Fatalf("Fail: %v", err)
			}
			if !retryable {
				break
			}
			if err := j.Stage(); err != nil {
				t.Fatalf("Stage retry: %v", err)
			}
			if err := j.Start("v1", at); err != nil {
				t.Fatalf("Start retry: %v", err)
			}
		}
		if err := j.RollBack(at); err != nil {
			t.Fatalf("RollBack: %v", err)
		}
	case UpgradeJobStatusCancelled:
		if err := j.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}
	return j
}

func TestUpgradeJobSM_ValidTransitions(t *testing.T) {
	at := time.Now().UTC()
	cases := []struct {
		name       string
		from       UpgradeJobStatus
		action     func(*FirmwareUpgradeJob) error
		wantStatus UpgradeJobStatus
	}{
		{"scheduled → staged", UpgradeJobStatusScheduled,
			func(j *FirmwareUpgradeJob) error { return j.Stage() }, UpgradeJobStatusStaged},
		{"staged → in_progress", UpgradeJobStatusStaged,
			func(j *FirmwareUpgradeJob) error { return j.Start("v1", at) }, UpgradeJobStatusInProgress},
		{"in_progress → succeeded", UpgradeJobStatusInProgress,
			func(j *FirmwareUpgradeJob) error { return j.Succeed(at) }, UpgradeJobStatusSucceeded},
		{"failed → rolled_back", UpgradeJobStatusFailed,
			func(j *FirmwareUpgradeJob) error { return j.RollBack(at) }, UpgradeJobStatusRolledBack},
		{"scheduled → cancelled", UpgradeJobStatusScheduled,
			func(j *FirmwareUpgradeJob) error { return j.Cancel() }, UpgradeJobStatusCancelled},
		{"staged → cancelled", UpgradeJobStatusStaged,
			func(j *FirmwareUpgradeJob) error { return j.Cancel() }, UpgradeJobStatusCancelled},
		// Idempotence
		{"staged → staged", UpgradeJobStatusStaged,
			func(j *FirmwareUpgradeJob) error { return j.Stage() }, UpgradeJobStatusStaged},
		{"succeeded → succeeded", UpgradeJobStatusSucceeded,
			func(j *FirmwareUpgradeJob) error { return j.Succeed(at) }, UpgradeJobStatusSucceeded},
		{"cancelled → cancelled", UpgradeJobStatusCancelled,
			func(j *FirmwareUpgradeJob) error { return j.Cancel() }, UpgradeJobStatusCancelled},
		{"rolled_back → rolled_back", UpgradeJobStatusRolledBack,
			func(j *FirmwareUpgradeJob) error { return j.RollBack(at) }, UpgradeJobStatusRolledBack},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := newJobAt(t, tc.from)
			if err := tc.action(j); err != nil {
				t.Fatalf("action: %v", err)
			}
			if j.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", j.Status, tc.wantStatus)
			}
		})
	}
}

func TestUpgradeJobSM_InvalidTransitions(t *testing.T) {
	at := time.Now().UTC()
	cases := []struct {
		name     string
		from     UpgradeJobStatus
		action   func(*FirmwareUpgradeJob) error
		wantCode string
	}{
		{"scheduled → succeeded (skip stage)", UpgradeJobStatusScheduled,
			func(j *FirmwareUpgradeJob) error { return j.Succeed(at) },
			"firmware_job.invalid_state_transition"},
		{"in_progress → cancelled", UpgradeJobStatusInProgress,
			func(j *FirmwareUpgradeJob) error { return j.Cancel() },
			"firmware_job.invalid_state_transition"},
		{"succeeded → rollback", UpgradeJobStatusSucceeded,
			func(j *FirmwareUpgradeJob) error { return j.RollBack(at) },
			"firmware_job.invalid_state_transition"},
		{"rolled_back → cancel", UpgradeJobStatusRolledBack,
			func(j *FirmwareUpgradeJob) error { return j.Cancel() },
			"firmware_job.invalid_state_transition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := newJobAt(t, tc.from)
			err := tc.action(j)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", j.Status)
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

func TestUpgradeJobFail_RetryBudget(t *testing.T) {
	j, err := NewFirmwareUpgradeJob(uuid.New(), nil, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("NewFirmwareUpgradeJob: %v", err)
	}
	j.MaxRetries = 2
	if err := j.Stage(); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := j.Start("v1", time.Now().UTC()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	retryable, err := j.Fail("boom 1")
	if err != nil || !retryable {
		t.Fatalf("first Fail should be retryable: retryable=%v err=%v", retryable, err)
	}
	if j.Status != UpgradeJobStatusScheduled {
		t.Errorf("after retryable Fail want scheduled, got %s", j.Status)
	}
	// Re-stage + start.
	if err := j.Stage(); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := j.Start("v1", time.Now().UTC()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	retryable, err = j.Fail("boom 2")
	if err != nil || retryable {
		t.Fatalf("second Fail should NOT be retryable: retryable=%v err=%v", retryable, err)
	}
	if j.Status != UpgradeJobStatusFailed {
		t.Errorf("after final Fail want failed, got %s", j.Status)
	}
}
