// Wave 118 — polygon overlap regression edge tests (TC-BR-*).

package usecase

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidatePolygonOverlap_NoOverlap(t *testing.T) {
	warnings := ValidatePolygonOverlap(1000, []PolygonRef{
		{BranchID: uuid.New(), AreaSqM: 800, IntersectionAreaSqM: 0},
	})
	if len(warnings) != 0 {
		t.Fatalf("no overlap should produce no warnings, got %v", warnings)
	}
}

func TestValidatePolygonOverlap_SmallOverlap_LowSeverity(t *testing.T) {
	// 2% overlap → low severity (filed but not warn-level)
	warnings := ValidatePolygonOverlap(1000, []PolygonRef{
		{BranchID: uuid.New(), AreaSqM: 1000, IntersectionAreaSqM: 20},
	})
	if len(warnings) != 1 {
		t.Fatalf("small overlap should produce 1 warning, got %d", len(warnings))
	}
	if warnings[0].Severity != OverlapSeverityLow {
		t.Fatalf("expected low severity, got %s", warnings[0].Severity)
	}
}

func TestValidatePolygonOverlap_MediumOverlap_WarnSeverity(t *testing.T) {
	// 20% overlap → warn severity
	warnings := ValidatePolygonOverlap(1000, []PolygonRef{
		{BranchID: uuid.New(), AreaSqM: 1000, IntersectionAreaSqM: 200},
	})
	if len(warnings) != 1 {
		t.Fatalf("medium overlap should produce 1 warning, got %d", len(warnings))
	}
	if warnings[0].Severity != OverlapSeverityWarn {
		t.Fatalf("expected warn severity, got %s", warnings[0].Severity)
	}
	if warnings[0].OverlapAreaRatio < 0.19 || warnings[0].OverlapAreaRatio > 0.21 {
		t.Fatalf("ratio off: want ~0.20, got %f", warnings[0].OverlapAreaRatio)
	}
}

func TestValidatePolygonOverlap_LargeOverlap_HighSeverity(t *testing.T) {
	// 75% overlap → high severity
	warnings := ValidatePolygonOverlap(1000, []PolygonRef{
		{BranchID: uuid.New(), AreaSqM: 1000, IntersectionAreaSqM: 750},
	})
	if len(warnings) != 1 {
		t.Fatalf("large overlap should produce 1 warning, got %d", len(warnings))
	}
	if warnings[0].Severity != OverlapSeverityHigh {
		t.Fatalf("expected high severity, got %s", warnings[0].Severity)
	}
}

func TestValidatePolygonOverlap_ContainsAndContained(t *testing.T) {
	// New polygon completely contains an existing one.
	containedExistingID := uuid.New()
	containingExistingID := uuid.New()
	warnings := ValidatePolygonOverlap(10000, []PolygonRef{
		{
			BranchID:            containedExistingID,
			AreaSqM:             500,
			IntersectionAreaSqM: 500,
			FullyContainedInNew: true,
		},
		{
			BranchID:            containingExistingID,
			AreaSqM:             20000,
			IntersectionAreaSqM: 10000,
			FullyContainsNew:    true,
		},
	})
	if len(warnings) != 2 {
		t.Fatalf("contains+contained should produce 2 warnings, got %d", len(warnings))
	}
	for _, w := range warnings {
		if w.Severity != OverlapSeverityHigh {
			t.Fatalf("containment should always be high severity, got %s for branch %v", w.Severity, w.ExistingBranchID)
		}
	}
}

func TestHasBlockingOverlap_NeverBlocks_Wave118Policy(t *testing.T) {
	warnings := []PolygonOverlapWarning{
		{Severity: OverlapSeverityHigh, Contains: true},
	}
	if HasBlockingOverlap(warnings) {
		t.Fatal("Wave 118 policy is pure-warning; HasBlockingOverlap must return false even for high severity")
	}
}

func TestAreaRatio_EdgeCases(t *testing.T) {
	if r := areaRatio(50, 100); r != 0.5 {
		t.Fatalf("ratio: want 0.5, got %f", r)
	}
	if r := areaRatio(50, 0); r != 0 {
		t.Fatalf("zero denom: want 0, got %f", r)
	}
	if r := areaRatio(150, 100); r != 1 {
		t.Fatalf("over-100%%: want clamp 1, got %f", r)
	}
	if r := areaRatio(-1, 100); r != 0 {
		t.Fatalf("negative: want 0, got %f", r)
	}
}
