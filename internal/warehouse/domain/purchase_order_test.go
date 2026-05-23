package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewPurchaseOrder_RejectsEmptyLines(t *testing.T) {
	_, _, err := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		nil, 11, "", nil, time.Now(),
	)
	if err == nil {
		t.Fatalf("expected error for empty lines")
	}
}

func TestNewPurchaseOrder_ComputesTotals(t *testing.T) {
	// 2 line items: 10 × 5000 = 50000, 3 × 20000 = 60000.
	// Subtotal = 110000, PPN 11% = 12100, total = 122100.
	po, lines, err := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 10, UnitCost: 5000},
			{StockItemID: uuid.New(), QuantityOrdered: 3, UnitCost: 20000},
		},
		11, "", nil, time.Now(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if po.Subtotal != 110000 {
		t.Fatalf("subtotal: want 110000, got %v", po.Subtotal)
	}
	if po.Total != 122100 {
		t.Fatalf("total: want 122100, got %v", po.Total)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0].LineNo != 1 || lines[1].LineNo != 2 {
		t.Fatalf("line numbers should be 1-indexed sequentially, got %d, %d",
			lines[0].LineNo, lines[1].LineNo)
	}
	if lines[0].LineSubtotal != 50000 {
		t.Fatalf("line[0] subtotal: want 50000, got %v", lines[0].LineSubtotal)
	}
}

func TestNewPurchaseOrder_DefaultsPPN(t *testing.T) {
	po, _, err := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 1, UnitCost: 100},
		},
		0, // zero → defaults to 11
		"", nil, time.Now(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if po.PPNRate != 11 {
		t.Fatalf("PPN default: want 11, got %v", po.PPNRate)
	}
}

func TestSubmit_OnlyFromDraft(t *testing.T) {
	po, _, _ := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 1, UnitCost: 1},
		},
		11, "", nil, time.Now(),
	)
	if err := po.Submit(uuid.New(), time.Now()); err != nil {
		t.Fatalf("draft → submitted should succeed: %v", err)
	}
	if po.Status != POStatusSubmitted {
		t.Fatalf("expected submitted, got %s", po.Status)
	}
	// Idempotent on already-submitted.
	if err := po.Submit(uuid.New(), time.Now()); err != nil {
		t.Fatalf("already-submitted Submit should be a no-op: %v", err)
	}
	// Force to a different state — re-submit should error.
	po.Status = POStatusApproved
	if err := po.Submit(uuid.New(), time.Now()); err == nil {
		t.Fatalf("Submit from approved should error")
	}
}

func TestApprove_OnlyFromSubmitted(t *testing.T) {
	po, _, _ := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 1, UnitCost: 1},
		},
		11, "", nil, time.Now(),
	)
	// Draft → approve should fail.
	if err := po.Approve(uuid.New(), time.Now()); err == nil {
		t.Fatalf("Approve from draft should error")
	}
	po.Status = POStatusSubmitted
	if err := po.Approve(uuid.New(), time.Now()); err != nil {
		t.Fatalf("Approve from submitted should succeed: %v", err)
	}
	if po.Status != POStatusApproved {
		t.Fatalf("expected approved, got %s", po.Status)
	}
	// Idempotent on already-approved.
	if err := po.Approve(uuid.New(), time.Now()); err != nil {
		t.Fatalf("already-approved Approve should be a no-op: %v", err)
	}
}

func TestReceivingThenClose(t *testing.T) {
	po, _, _ := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 1, UnitCost: 1},
		},
		11, "", nil, time.Now(),
	)
	po.Status = POStatusApproved
	if err := po.MarkReceiving(time.Now()); err != nil {
		t.Fatalf("approved → receiving should succeed: %v", err)
	}
	if po.Status != POStatusReceiving {
		t.Fatalf("expected receiving, got %s", po.Status)
	}
	// Idempotent.
	if err := po.MarkReceiving(time.Now()); err != nil {
		t.Fatalf("already-receiving MarkReceiving should be a no-op: %v", err)
	}
	if err := po.Close(time.Now()); err != nil {
		t.Fatalf("receiving → closed should succeed: %v", err)
	}
	if po.Status != POStatusClosed {
		t.Fatalf("expected closed, got %s", po.Status)
	}
	if po.ClosedAt == nil {
		t.Fatalf("ClosedAt should be set")
	}
	// Close from draft is invalid.
	po2, _, _ := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 1, UnitCost: 1},
		},
		11, "", nil, time.Now(),
	)
	if err := po2.Close(time.Now()); err == nil {
		t.Fatalf("Close from draft should error")
	}
}

func TestCancel_RequiresReason(t *testing.T) {
	po, _, _ := NewPurchaseOrder(
		uuid.New(), uuid.New(), uuid.New(),
		[]PurchaseOrderLineInput{
			{StockItemID: uuid.New(), QuantityOrdered: 1, UnitCost: 1},
		},
		11, "", nil, time.Now(),
	)
	if err := po.Cancel(uuid.New(), "", time.Now()); err == nil {
		t.Fatalf("Cancel without reason should error")
	}
	if err := po.Cancel(uuid.New(), "over budget", time.Now()); err != nil {
		t.Fatalf("Cancel with reason should succeed: %v", err)
	}
	if po.Status != POStatusCancelled {
		t.Fatalf("expected cancelled, got %s", po.Status)
	}
}
