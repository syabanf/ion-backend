package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBulkPlanChangeItem_StatusMachine(t *testing.T) {
	it := &BulkPlanChangeItem{
		ID:           uuid.New(),
		BulkJobID:    uuid.New(),
		CustomerID:   uuid.New(),
		TargetPlanID: uuid.New(),
		Status:       BPCItemQueued,
	}
	if err := it.MarkValidating(); err != nil {
		t.Fatalf("MarkValidating: %v", err)
	}
	if err := it.MarkValidated(); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}
	if err := it.MarkProcessing(); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}
	if err := it.MarkSucceeded(time.Time{}); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if !it.IsTerminal() {
		t.Fatalf("should be terminal")
	}
	if err := it.MarkSucceeded(time.Now()); err == nil {
		t.Fatalf("re-success on terminal should error")
	}
}

func TestBulkPlanChangeItem_SkipFromQueued(t *testing.T) {
	it := &BulkPlanChangeItem{Status: BPCItemQueued}
	if err := it.MarkSkipped("noop"); err != nil {
		t.Fatalf("MarkSkipped: %v", err)
	}
	if it.Status != BPCItemSkipped {
		t.Errorf("want skipped, got %s", it.Status)
	}
	if it.ErrorMsg != "noop" {
		t.Errorf("reason: want noop, got %s", it.ErrorMsg)
	}
}

func TestBulkODPMigrationItem_StateMachine(t *testing.T) {
	it := &BulkODPMigrationItem{
		Status:      BOMItemQueued,
		ToOLTPortID: uuid.New(),
	}
	if err := it.MarkValidating(); err != nil {
		t.Fatalf("MarkValidating: %v", err)
	}
	if err := it.MarkValidated(); err != nil {
		t.Fatalf("MarkValidated: %v", err)
	}
	if err := it.MarkStaged(uuid.New()); err != nil {
		t.Fatalf("MarkStaged: %v", err)
	}
	if err := it.MarkMigrated(time.Time{}); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}
	if !it.IsTerminal() {
		t.Fatalf("should be terminal")
	}
}

func TestBulkWOCreationItem_DuplicatePath(t *testing.T) {
	it := &BulkWOCreationItem{Status: BWOItemQueued}
	if err := it.MarkDuplicate("open"); err != nil {
		t.Fatalf("MarkDuplicate: %v", err)
	}
	if it.Status != BWOItemDuplicate {
		t.Errorf("want duplicate, got %s", it.Status)
	}
}

func TestBulkWOCreationItem_CreatedPath(t *testing.T) {
	it := &BulkWOCreationItem{Status: BWOItemQueued}
	_ = it.MarkValidating()
	_ = it.MarkValidated()
	woID := uuid.New()
	if err := it.MarkCreated(woID, time.Time{}); err != nil {
		t.Fatalf("MarkCreated: %v", err)
	}
	if it.CreatedWOID == nil || *it.CreatedWOID != woID {
		t.Errorf("CreatedWOID not stamped")
	}
}
