// Wave 118 — Team Lead conflict-detect for broadband WO scheduling
// (TC-TLP-* regression edge).
//
// Wave 96 shipped the enterprise EWO version (internal/enterprise/usecase/
// tl_scheduling.go::checkOverlap). Broadband WOs use the same TL pool but
// historically didn't enforce overlap detection. The audit flagged this
// as a regression edge.
//
// This file is a pure-domain helper. Production callers fetch the
// existing assignments for a (team_lead_id, time window) and pass them
// in; the helper compares ranges and returns any conflicts.
//
// Re-pairing audit (the second half of the audit's TL gap): every
// conflict-returning call MUST be audited so an admin can review the
// re-pairing decisions. This helper returns the conflicts; the caller
// is responsible for emitting the audit row.

package usecase

import (
	"time"

	"github.com/google/uuid"
)

// WOScheduleAssignment is the narrow projection used by the conflict
// checker. Callers pull from internal/field's WO repo (or operations'
// scheduling table when Wave 122 lands it).
type WOScheduleAssignment struct {
	WOID         uuid.UUID
	TeamLeadID   uuid.UUID
	Start        time.Time
	End          time.Time
	// CustomerID is informational — surfaced in the conflict report so
	// the admin sees what's on the other side without an extra lookup.
	CustomerID *uuid.UUID
}

// WOConflictReport names the conflict for the caller. Each entry is a
// distinct existing assignment that overlaps the candidate window.
type WOConflictReport struct {
	ExistingWOID uuid.UUID
	ExistingStart time.Time
	ExistingEnd   time.Time
	CustomerID    *uuid.UUID
}

// FindTLOverlaps returns the existing assignments that overlap the
// candidate (start, end) window for the given team lead. Excludes the
// optional excludeWOID so a reschedule of an existing assignment doesn't
// collide with its own old row.
//
// Ranges overlap when (start < existing.End AND end > existing.Start) —
// matches the half-open convention used elsewhere (e.g. enterprise EWO
// scheduling). Equal end-to-start endpoints do NOT overlap.
func FindTLOverlaps(
	teamLeadID uuid.UUID,
	start, end time.Time,
	existing []WOScheduleAssignment,
	excludeWOID *uuid.UUID,
) []WOConflictReport {
	if end.Before(start) || end.Equal(start) {
		return nil
	}
	var out []WOConflictReport
	for _, a := range existing {
		if a.TeamLeadID != teamLeadID {
			continue
		}
		if excludeWOID != nil && a.WOID == *excludeWOID {
			continue
		}
		if rangesOverlap(start, end, a.Start, a.End) {
			out = append(out, WOConflictReport{
				ExistingWOID:  a.WOID,
				ExistingStart: a.Start,
				ExistingEnd:   a.End,
				CustomerID:    a.CustomerID,
			})
		}
	}
	return out
}

// rangesOverlap returns true when [aStart, aEnd) and [bStart, bEnd)
// share any instant.
func rangesOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && aEnd.After(bStart)
}
