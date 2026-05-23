// Wave 118 — TL conflict-detect tests (TC-TLP-* regression edge).

package usecase

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFindTLOverlaps_NoConflict(t *testing.T) {
	tl := uuid.New()
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	existing := []WOScheduleAssignment{
		{
			WOID:       uuid.New(),
			TeamLeadID: tl,
			Start:      time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC),
			End:        time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC),
		},
	}
	conflicts := FindTLOverlaps(tl, start, end, existing, nil)
	if len(conflicts) != 0 {
		t.Fatalf("non-overlapping windows: want 0 conflicts, got %d", len(conflicts))
	}
}

func TestFindTLOverlaps_SameTL_Overlap_Conflict(t *testing.T) {
	tl := uuid.New()
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	existingWOID := uuid.New()
	existing := []WOScheduleAssignment{
		{
			WOID:       existingWOID,
			TeamLeadID: tl,
			Start:      time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
			End:        time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	conflicts := FindTLOverlaps(tl, start, end, existing, nil)
	if len(conflicts) != 1 {
		t.Fatalf("overlapping same-TL: want 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].ExistingWOID != existingWOID {
		t.Fatalf("conflict reports wrong WO: %v", conflicts[0].ExistingWOID)
	}
}

func TestFindTLOverlaps_DifferentTL_NoConflict(t *testing.T) {
	tlA := uuid.New()
	tlB := uuid.New()
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	existing := []WOScheduleAssignment{
		{
			WOID:       uuid.New(),
			TeamLeadID: tlB, // different TL
			Start:      time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
			End:        time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	conflicts := FindTLOverlaps(tlA, start, end, existing, nil)
	if len(conflicts) != 0 {
		t.Fatalf("different-TL overlap should not conflict, got %d", len(conflicts))
	}
}

func TestFindTLOverlaps_ExcludeSelf_NoConflict(t *testing.T) {
	tl := uuid.New()
	selfWOID := uuid.New()
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	existing := []WOScheduleAssignment{
		{
			WOID:       selfWOID,
			TeamLeadID: tl,
			Start:      start,
			End:        end,
		},
	}
	conflicts := FindTLOverlaps(tl, start, end, existing, &selfWOID)
	if len(conflicts) != 0 {
		t.Fatalf("self-exclude should not conflict; got %d", len(conflicts))
	}
}

func TestFindTLOverlaps_AdjacentRanges_NoOverlap(t *testing.T) {
	tl := uuid.New()
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	// existing ends exactly when new starts — half-open ranges, no overlap.
	existing := []WOScheduleAssignment{
		{
			WOID:       uuid.New(),
			TeamLeadID: tl,
			Start:      time.Date(2026, 6, 1, 7, 0, 0, 0, time.UTC),
			End:        start,
		},
	}
	conflicts := FindTLOverlaps(tl, start, end, existing, nil)
	if len(conflicts) != 0 {
		t.Fatalf("adjacent ranges should not conflict, got %d", len(conflicts))
	}
}
