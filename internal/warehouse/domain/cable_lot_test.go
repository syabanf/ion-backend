package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewCableLot_ValidatesInputs(t *testing.T) {
	if _, err := NewCableLot(uuid.Nil, 1000); err == nil {
		t.Fatal("expected error on nil item_id")
	}
	if _, err := NewCableLot(uuid.New(), 0); err == nil {
		t.Fatal("expected error on non-positive length")
	}
	lot, err := NewCableLot(uuid.New(), 1000)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if lot.RemainingLengthMeters != 1000 {
		t.Fatalf("expected remaining=total at intake, got %f", lot.RemainingLengthMeters)
	}
	if lot.Status != CableLotStatusInStock {
		t.Fatalf("expected in_stock, got %s", lot.Status)
	}
}

func TestCableLot_CutSegment_HappyPath(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 1000)
	by := uuid.New()
	cut, err := lot.CutSegment(50, nil, &by)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lot.RemainingLengthMeters != 950 {
		t.Fatalf("expected remaining=950, got %f", lot.RemainingLengthMeters)
	}
	if cut.CutLengthMeters != 50 {
		t.Fatalf("expected cut=50, got %f", cut.CutLengthMeters)
	}
	if lot.Status != CableLotStatusAllocated {
		t.Fatalf("expected allocated after first cut, got %s", lot.Status)
	}
}

func TestCableLot_CutSegment_Exhaustion(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 100)
	if _, err := lot.CutSegment(100, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lot.Status != CableLotStatusConsumed {
		t.Fatalf("expected consumed when remaining=0, got %s", lot.Status)
	}
	if _, err := lot.CutSegment(1, nil, nil); err == nil {
		t.Fatal("expected error cutting from consumed lot")
	}
}

func TestCableLot_CutSegment_RefusesOverdraft(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 100)
	if _, err := lot.CutSegment(150, nil, nil); err == nil {
		t.Fatal("expected refusal on insufficient remaining")
	}
	if lot.RemainingLengthMeters != 100 {
		t.Fatalf("expected remaining unchanged on refusal, got %f", lot.RemainingLengthMeters)
	}
}

func TestCableLot_CutSegment_RefusesNonPositive(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 100)
	if _, err := lot.CutSegment(0, nil, nil); err == nil {
		t.Fatal("expected refusal on zero cut")
	}
	if _, err := lot.CutSegment(-5, nil, nil); err == nil {
		t.Fatal("expected refusal on negative cut")
	}
}

func TestCableLot_Dispose_FromInStock(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 100)
	if err := lot.Dispose("damaged drum"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lot.Status != CableLotStatusDisposed {
		t.Fatalf("expected disposed, got %s", lot.Status)
	}
	if _, err := lot.CutSegment(10, nil, nil); err == nil {
		t.Fatal("expected refusal cutting from disposed lot")
	}
}

func TestCableLot_Dispose_AfterConsumedRefused(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 50)
	if _, err := lot.CutSegment(50, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := lot.Dispose("oops"); err == nil {
		t.Fatal("expected refusal disposing already-consumed lot")
	}
}

func TestCableLot_MultipleCuts(t *testing.T) {
	lot, _ := NewCableLot(uuid.New(), 1000)
	for i := 0; i < 4; i++ {
		if _, err := lot.CutSegment(100, nil, nil); err != nil {
			t.Fatalf("unexpected error on cut %d: %v", i, err)
		}
	}
	if lot.RemainingLengthMeters != 600 {
		t.Fatalf("expected remaining=600 after 4×100 cuts, got %f", lot.RemainingLengthMeters)
	}
	if lot.Status != CableLotStatusAllocated {
		t.Fatalf("expected still allocated, got %s", lot.Status)
	}
}
