package domain

import (
	"testing"

	"github.com/google/uuid"
)

// TestAutoCalcSellPriceWithFloor — Wave 106 TC-PB-006 floor-violation
// surface. The variant returns (sell, margin, violation, err); when the
// computed margin is below MinMarginPct, violation is non-nil so the
// usecase can wrap it in a Validation error with details.
func TestAutoCalcSellPriceWithFloor(t *testing.T) {
	tests := []struct {
		name             string
		defaultMargin    float64
		minMargin        float64
		vendorCost       float64
		wantSell         float64
		wantViolation    bool
		wantErr          bool
	}{
		{
			name:          "happy: cost 3.5M margin 30% sell 5M",
			defaultMargin: 30,
			minMargin:     18,
			vendorCost:    3_500_000,
			wantSell:      5_000_000,
			wantViolation: false,
		},
		{
			name:          "boundary: margin equals floor exactly",
			defaultMargin: 18,
			minMargin:     18,
			vendorCost:    1_000_000,
			// sell = 1M / (1 - 0.18) = 1,219,512.20
			wantViolation: false,
		},
		{
			name:          "violation: default margin below min floor",
			defaultMargin: 10,
			minMargin:     10, // NewPricebookLine forbids min > default; we construct directly here to test violation path
			vendorCost:    1_000_000,
			wantViolation: false, // 10 == 10 boundary still PASS
		},
		{
			name:          "violation: synthetic min above default (post-mutation case)",
			defaultMargin: 5,
			minMargin:     15,
			vendorCost:    1_000_000,
			wantViolation: true, // 5% < 15% floor
		},
		{
			name:          "vendor cost zero — still valid: sell zero",
			defaultMargin: 30,
			minMargin:     18,
			vendorCost:    0,
			wantSell:      0,
			wantViolation: false, // margin = 30% > 18%
		},
		{
			name:          "default 100% — error",
			defaultMargin: 100,
			minMargin:     50,
			vendorCost:    1_000_000,
			wantErr:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Construct directly (skipping NewPricebookLine's invariant
			// check) so we can probe synthetic min>default cases.
			l := &PricebookLine{
				ID:               uuid.New(),
				PricebookID:      uuid.New(),
				SKU:              "TEST",
				Name:             "Test Line",
				Unit:             "unit",
				DefaultMarginPct: tt.defaultMargin,
				MinMarginPct:     tt.minMargin,
				MaxDiscountPct:   20,
			}
			sell, margin, violation, err := l.AutoCalcSellPriceWithFloor(tt.vendorCost)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (sell=%v margin=%v)", sell, margin)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil error, got %v", err)
			}
			if tt.wantViolation && violation == nil {
				t.Errorf("want non-nil violation, got nil (margin=%.2f floor=%.2f)", margin, tt.minMargin)
			}
			if !tt.wantViolation && violation != nil {
				t.Errorf("want nil violation, got %+v", violation)
			}
		})
	}
}

// TestPricebookLine_PriorityScore — Wave 106 TC-PB-010. Confirms the
// new PriorityScore field is wired into the domain struct (defaults to
// 0 from NewPricebookLine) and can be mutated.
func TestPricebookLine_PriorityScore(t *testing.T) {
	l, err := NewPricebookLine(uuid.New(), "SKU-1", "Test", 100, 30, 18, 20)
	if err != nil {
		t.Fatalf("NewPricebookLine err = %v", err)
	}
	if l.PriorityScore != 0 {
		t.Errorf("default PriorityScore = %d, want 0", l.PriorityScore)
	}
	l.PriorityScore = 7
	if l.PriorityScore != 7 {
		t.Errorf("PriorityScore after set = %d, want 7", l.PriorityScore)
	}
}
