package domain

import (
	"testing"
)

func TestNewBulkJob(t *testing.T) {
	tests := []struct {
		name    string
		kind    BulkJobKind
		wantErr bool
	}{
		{"plan_change", BulkJobPlanChange, false},
		{"odp_migration", BulkJobODPMigration, false},
		{"wo_creation", BulkJobWOCreation, false},
		{"wo_cancellation", BulkJobWOCancellation, false},
		{"customer_segment_export", BulkJobCustomerSegmentExport, false},
		{"invalid", BulkJobKind("nonsense"), true},
		{"empty", BulkJobKind(""), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j, err := NewBulkJob(tc.kind, false, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if j.Status != BulkJobStatusPending {
				t.Errorf("status: want pending, got %s", j.Status)
			}
			if j.Kind != tc.kind {
				t.Errorf("kind: want %s, got %s", tc.kind, j.Kind)
			}
		})
	}
}

func TestBulkJob_StatusMachine(t *testing.T) {
	j, _ := NewBulkJob(BulkJobPlanChange, false, nil)

	// pending → validating
	if err := j.MarkValidating(); err != nil {
		t.Fatalf("MarkValidating: %v", err)
	}
	if j.Status != BulkJobStatusValidating {
		t.Errorf("want validating, got %s", j.Status)
	}

	// idempotent
	if err := j.MarkValidating(); err != nil {
		t.Fatalf("second MarkValidating: %v", err)
	}

	// validating → running
	if err := j.MarkRunning(); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if j.StartedAt == nil {
		t.Fatalf("StartedAt should be set on first running transition")
	}

	// re-running is a no-op
	if err := j.MarkRunning(); err != nil {
		t.Fatalf("second MarkRunning: %v", err)
	}

	// running → completed
	if err := j.MarkCompleted(); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	if !j.IsTerminal() {
		t.Fatalf("completed should be terminal")
	}

	// terminal lockout
	if err := j.MarkRunning(); err == nil {
		t.Fatalf("should not be able to run a completed job")
	}
}

func TestBulkJob_MarkCancelled(t *testing.T) {
	j, _ := NewBulkJob(BulkJobPlanChange, false, nil)
	_ = j.MarkRunning()
	if err := j.MarkCancelled(); err != nil {
		t.Fatalf("MarkCancelled: %v", err)
	}
	if j.Status != BulkJobStatusCancelled {
		t.Errorf("want cancelled, got %s", j.Status)
	}
	if err := j.MarkCancelled(); err == nil {
		t.Errorf("second cancel should error (terminal)")
	}
}

func TestBulkJob_MarkFailed(t *testing.T) {
	j, _ := NewBulkJob(BulkJobODPMigration, false, nil)
	_ = j.MarkRunning()
	if err := j.MarkFailed("downstream offline"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if got := j.ErrorSummary["reason"]; got != "downstream offline" {
		t.Errorf("reason: want 'downstream offline', got %v", got)
	}
}

func TestBulkJob_RecordItem(t *testing.T) {
	j, _ := NewBulkJob(BulkJobWOCreation, false, nil)
	j.RecordItem(true, false)
	j.RecordItem(true, false)
	j.RecordItem(false, true)
	j.RecordItem(false, false)
	if j.ProcessedItems != 4 {
		t.Errorf("processed: want 4, got %d", j.ProcessedItems)
	}
	if j.SucceededItems != 2 {
		t.Errorf("succeeded: want 2, got %d", j.SucceededItems)
	}
	if j.SkippedItems != 1 {
		t.Errorf("skipped: want 1, got %d", j.SkippedItems)
	}
	if j.FailedItems != 1 {
		t.Errorf("failed: want 1, got %d", j.FailedItems)
	}
}

func TestBulkJob_Finalize_AllSucceeded(t *testing.T) {
	j, _ := NewBulkJob(BulkJobPlanChange, false, nil)
	_ = j.MarkRunning()
	j.TotalItems = 3
	j.SucceededItems = 3
	j.ProcessedItems = 3
	if err := j.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if j.Status != BulkJobStatusCompleted {
		t.Errorf("want completed, got %s", j.Status)
	}
}

func TestBulkJob_Finalize_AllFailed(t *testing.T) {
	j, _ := NewBulkJob(BulkJobPlanChange, false, nil)
	_ = j.MarkRunning()
	j.TotalItems = 3
	j.FailedItems = 3
	j.ProcessedItems = 3
	if err := j.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if j.Status != BulkJobStatusFailed {
		t.Errorf("want failed, got %s", j.Status)
	}
}

func TestBulkJob_Finalize_Partial(t *testing.T) {
	j, _ := NewBulkJob(BulkJobWOCreation, false, nil)
	_ = j.MarkRunning()
	j.TotalItems = 4
	j.SucceededItems = 2
	j.FailedItems = 1
	j.SkippedItems = 1
	j.ProcessedItems = 4
	if err := j.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if j.Status != BulkJobStatusPartial {
		t.Errorf("want partial, got %s", j.Status)
	}
}

func TestBulkJob_Finalize_Empty(t *testing.T) {
	j, _ := NewBulkJob(BulkJobPlanChange, false, nil)
	_ = j.MarkRunning()
	// zero items → completed (treated as no-op success)
	if err := j.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if j.Status != BulkJobStatusCompleted {
		t.Errorf("want completed for empty job, got %s", j.Status)
	}
}

func TestBulkJob_Finalize_Idempotent(t *testing.T) {
	j, _ := NewBulkJob(BulkJobPlanChange, false, nil)
	_ = j.MarkRunning()
	_ = j.MarkCompleted()
	if err := j.Finalize(); err != nil {
		t.Errorf("Finalize on terminal job should be nil, got %v", err)
	}
}
