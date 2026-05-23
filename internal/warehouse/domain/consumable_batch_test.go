package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewConsumableBatch_ValidatesInputs(t *testing.T) {
	if _, err := NewConsumableBatch(uuid.Nil, "B-1", 10); err == nil {
		t.Fatal("expected error on nil item_id")
	}
	if _, err := NewConsumableBatch(uuid.New(), "", 10); err == nil {
		t.Fatal("expected error on empty batch_no")
	}
	if _, err := NewConsumableBatch(uuid.New(), "B-1", 0); err == nil {
		t.Fatal("expected error on zero qty")
	}
	b, err := NewConsumableBatch(uuid.New(), "B-1", 100)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if b.RemainingQty != 100 || b.Status != ConsumableBatchStatusInStock {
		t.Fatalf("expected fresh batch state, got %d / %s", b.RemainingQty, b.Status)
	}
}

func TestConsumableBatch_Consume_HappyPath(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 10)
	log, err := b.Consume(3, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.RemainingQty != 7 {
		t.Fatalf("expected remaining=7, got %d", b.RemainingQty)
	}
	if log.QtyConsumed != 3 {
		t.Fatalf("expected log qty=3, got %d", log.QtyConsumed)
	}
	if b.Status != ConsumableBatchStatusAllocated {
		t.Fatalf("expected allocated after first consume, got %s", b.Status)
	}
}

func TestConsumableBatch_Consume_Exhaustion(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 5)
	if _, err := b.Consume(5, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Status != ConsumableBatchStatusConsumed {
		t.Fatalf("expected consumed status, got %s", b.Status)
	}
	if _, err := b.Consume(1, nil, nil); err == nil {
		t.Fatal("expected refusal consuming from exhausted batch")
	}
}

func TestConsumableBatch_Consume_RefusesOverdraft(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 5)
	if _, err := b.Consume(10, nil, nil); err == nil {
		t.Fatal("expected refusal consuming more than remaining")
	}
	if b.RemainingQty != 5 {
		t.Fatalf("expected remaining unchanged on refusal, got %d", b.RemainingQty)
	}
}

func TestConsumableBatch_Consume_RefusesNonPositive(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 5)
	if _, err := b.Consume(0, nil, nil); err == nil {
		t.Fatal("expected refusal on zero qty")
	}
	if _, err := b.Consume(-1, nil, nil); err == nil {
		t.Fatal("expected refusal on negative qty")
	}
}

func TestConsumableBatch_IsExpired(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 5)
	if b.IsExpired(time.Now()) {
		t.Fatal("expected non-expired when ExpiryDate=nil")
	}
	past := time.Now().Add(-24 * time.Hour)
	b.ExpiryDate = &past
	if !b.IsExpired(time.Now()) {
		t.Fatal("expected expired when expiry_date is in the past")
	}
}

func TestConsumableBatch_MarkExpired_Transitions(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 5)
	if err := b.MarkExpired(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Status != ConsumableBatchStatusExpired {
		t.Fatalf("expected expired, got %s", b.Status)
	}
	if _, err := b.Consume(1, nil, nil); err == nil {
		t.Fatal("expected refusal consuming from expired batch")
	}
}

func TestConsumableBatch_MarkExpired_RefusedAfterConsumed(t *testing.T) {
	b, _ := NewConsumableBatch(uuid.New(), "B-1", 1)
	if _, err := b.Consume(1, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.MarkExpired(); err == nil {
		t.Fatal("expected refusal marking expired on already-consumed batch")
	}
}
