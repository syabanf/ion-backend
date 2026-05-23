package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewBulkGenerationJob_StartsAsPending(t *testing.T) {
	j, err := NewBulkGenerationJob(BulkJobMonthlyCycle, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobStatusPending {
		t.Errorf("expected pending, got %s", j.Status)
	}
}

func TestNewBulkGenerationJob_RejectsBadKind(t *testing.T) {
	if _, err := NewBulkGenerationJob("bogus", nil, nil); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestBulkJob_StartIdempotent(t *testing.T) {
	j, _ := NewBulkGenerationJob(BulkJobMonthlyCycle, nil, nil)
	if err := j.Start(time.Now()); err != nil {
		t.Fatal(err)
	}
	// second call is a no-op (returns nil)
	if err := j.Start(time.Now()); err != nil {
		t.Errorf("second Start should noop, got %v", err)
	}
}

func TestBulkJob_FinishCompletedAllSuccess(t *testing.T) {
	j, _ := NewBulkGenerationJob(BulkJobMonthlyCycle, nil, nil)
	_ = j.Start(time.Now())
	items := []BulkGenerationItem{
		{Status: ItemStatusGenerated},
		{Status: ItemStatusGenerated},
		{Status: ItemStatusGenerated},
	}
	if err := j.Finish(items, time.Now()); err != nil {
		t.Fatal(err)
	}
	if j.Status != JobStatusCompleted {
		t.Errorf("expected completed, got %s", j.Status)
	}
	if j.TotalGenerated != 3 {
		t.Errorf("expected 3 generated, got %d", j.TotalGenerated)
	}
}

func TestBulkJob_FinishFailedAllError(t *testing.T) {
	j, _ := NewBulkGenerationJob(BulkJobMonthlyCycle, nil, nil)
	_ = j.Start(time.Now())
	items := []BulkGenerationItem{
		{Status: ItemStatusFailed},
		{Status: ItemStatusFailed},
	}
	if err := j.Finish(items, time.Now()); err != nil {
		t.Fatal(err)
	}
	if j.Status != JobStatusFailed {
		t.Errorf("expected failed, got %s", j.Status)
	}
}

func TestBulkJob_FinishPartialMixed(t *testing.T) {
	j, _ := NewBulkGenerationJob(BulkJobMonthlyCycle, nil, nil)
	_ = j.Start(time.Now())
	items := []BulkGenerationItem{
		{Status: ItemStatusGenerated},
		{Status: ItemStatusFailed},
		{Status: ItemStatusSkipped},
	}
	if err := j.Finish(items, time.Now()); err != nil {
		t.Fatal(err)
	}
	if j.Status != JobStatusPartial {
		t.Errorf("expected partial, got %s", j.Status)
	}
}

func TestBulkJob_FinishConflictOnReFinish(t *testing.T) {
	j, _ := NewBulkGenerationJob(BulkJobMonthlyCycle, nil, nil)
	_ = j.Start(time.Now())
	items := []BulkGenerationItem{{Status: ItemStatusGenerated}}
	_ = j.Finish(items, time.Now())
	if err := j.Finish(items, time.Now()); err == nil {
		t.Error("expected conflict on re-finish")
	}
}

func TestItemStatusBucket_CountsAllStatuses(t *testing.T) {
	items := []BulkGenerationItem{
		{Status: ItemStatusQueued},
		{Status: ItemStatusQueued},
		{Status: ItemStatusGenerated},
		{Status: ItemStatusFailed},
		{Status: ItemStatusSkipped},
	}
	b := ItemStatusBucket(items)
	if b.Queued != 2 || b.Generated != 1 || b.Failed != 1 || b.Skipped != 1 {
		t.Errorf("bucket counts wrong: %+v", b)
	}
}

func TestBulkItem_MarkGeneratedFromQueued(t *testing.T) {
	it := BulkGenerationItem{Status: ItemStatusQueued}
	invID := uuid.New()
	if err := it.MarkGenerated(invID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if it.Status != ItemStatusGenerated {
		t.Errorf("expected generated, got %s", it.Status)
	}
	if it.InvoiceID == nil || *it.InvoiceID != invID {
		t.Error("invoice id not bound")
	}
}

func TestBulkItem_MarkGeneratedFromNonQueuedConflicts(t *testing.T) {
	it := BulkGenerationItem{Status: ItemStatusGenerated}
	if err := it.MarkGenerated(uuid.New(), time.Now()); err == nil {
		t.Error("expected conflict")
	}
}

func TestBulkItem_MarkFailed(t *testing.T) {
	it := BulkGenerationItem{Status: ItemStatusQueued}
	if err := it.MarkFailed("boom"); err != nil {
		t.Fatal(err)
	}
	if it.Status != ItemStatusFailed || it.ErrorMsg != "boom" {
		t.Errorf("unexpected: %+v", it)
	}
}

func TestBulkItem_MarkSkipped(t *testing.T) {
	it := BulkGenerationItem{Status: ItemStatusQueued}
	if err := it.MarkSkipped("inactive customer"); err != nil {
		t.Fatal(err)
	}
	if it.Status != ItemStatusSkipped {
		t.Errorf("expected skipped, got %s", it.Status)
	}
}
