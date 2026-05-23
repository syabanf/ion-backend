// Package usecase — operations bounded context use cases.
//
// Wave 118 — polygon overlap warning (TC-BR-* regression edge).
//
// Branch polygons should not silently overlap. The audit (Wave 110) flagged
// this as a regression edge: a new branch polygon that overlaps an existing
// one >5% triggers a warning, but does NOT block (overlaps are sometimes
// intentional during transition periods). Larger overlaps (>50%) escalate
// to "high" severity for admin review.
//
// This is a pure-domain helper — no DB or HTTP dependency. The branch-
// create flow in internal/identity/usecase/service.go::CreateBranch calls
// this method before persisting and surfaces any warnings via the audit
// log so admins can review.
package usecase

import (
	"math"

	"github.com/google/uuid"
)

// PolygonOverlapSeverity is the bucket the overlap percentage falls into.
type PolygonOverlapSeverity string

const (
	OverlapSeverityNone PolygonOverlapSeverity = "none"
	OverlapSeverityLow  PolygonOverlapSeverity = "low"  // <5%
	OverlapSeverityWarn PolygonOverlapSeverity = "warn" // 5-50%
	OverlapSeverityHigh PolygonOverlapSeverity = "high" // >50%
)

// PolygonOverlapWarning is a single (new vs. existing) overlap report.
type PolygonOverlapWarning struct {
	ExistingBranchID uuid.UUID
	OverlapAreaRatio float64
	Severity         PolygonOverlapSeverity
	// Contains is true when the new polygon completely contains an
	// existing one (ratio = 1.0 vs the existing polygon's area).
	Contains bool
	// Contained is true when an existing polygon completely contains
	// the new one (ratio = 1.0 vs the new polygon's area).
	Contained bool
}

// PolygonRef is a minimal projection of an existing branch polygon for
// the overlap check. Production callers pass the GeoJSON; the
// ratio + containment fields are computed via PostGIS — this domain
// helper accepts pre-computed area values to stay testable without a
// database dependency.
type PolygonRef struct {
	BranchID uuid.UUID
	// AreaSqM is the polygon's area in square meters.
	AreaSqM float64
	// IntersectionAreaSqM is the area of the intersection between this
	// existing polygon and the new polygon being validated. The caller
	// computes this via PostGIS ST_Area(ST_Intersection(...)).
	IntersectionAreaSqM float64
	// FullyContainsNew is true when this existing polygon ⊇ new polygon.
	FullyContainsNew bool
	// FullyContainedInNew is true when this existing polygon ⊆ new polygon.
	FullyContainedInNew bool
}

// ValidatePolygonOverlap returns the per-existing-polygon warnings.
// newAreaSqM is the new polygon's total area; pass <= 0 if unknown
// (the helper falls back to ratio-vs-existing-area).
//
// Thresholds (matching the audit's TC-BR-* expectations):
//   - <5% intersection → "low" severity (logged but not flagged)
//   - 5-50% → "warn" severity (admin review)
//   - >50% → "high" severity (admin review + manual approval)
//   - 100% containment (either way) → always "high"
func ValidatePolygonOverlap(newAreaSqM float64, existing []PolygonRef) []PolygonOverlapWarning {
	if len(existing) == 0 {
		return nil
	}
	var out []PolygonOverlapWarning
	for _, p := range existing {
		// Compute the ratio against the larger of the two areas so a
		// tiny existing polygon entirely inside a giant new one is
		// caught (ratio against new area = small, but containment is
		// total — we flag it as "high" via the contained flag).
		denom := p.AreaSqM
		if newAreaSqM > denom {
			denom = newAreaSqM
		}
		if denom <= 0 {
			continue
		}
		ratio := 0.0
		if p.IntersectionAreaSqM > 0 {
			ratio = p.IntersectionAreaSqM / denom
		}
		w := PolygonOverlapWarning{
			ExistingBranchID: p.BranchID,
			OverlapAreaRatio: ratio,
			Contains:         p.FullyContainedInNew,
			Contained:        p.FullyContainsNew,
		}
		switch {
		case p.FullyContainsNew || p.FullyContainedInNew:
			w.Severity = OverlapSeverityHigh
		case ratio > 0.5:
			w.Severity = OverlapSeverityHigh
		case ratio >= 0.05:
			w.Severity = OverlapSeverityWarn
		case ratio > 0:
			w.Severity = OverlapSeverityLow
		default:
			w.Severity = OverlapSeverityNone
		}
		if w.Severity != OverlapSeverityNone {
			out = append(out, w)
		}
	}
	return out
}

// HasBlockingOverlap reports whether any of the warnings would force the
// admin to approve before the branch can be created. Today this is
// pure-warning (the caller decides), but the helper exists so future
// policy changes can flip the rule by changing this one function.
func HasBlockingOverlap(_ []PolygonOverlapWarning) bool {
	// Wave 118: never blocking — pure warning surface.
	return false
}

// areaRatio returns the ratio of intersection to denom, clamped to [0, 1].
// Exported only via the helper above; kept here so tests can reach in for
// edge-case checks via the internal package.
func areaRatio(intersection, denom float64) float64 {
	if denom <= 0 {
		return 0
	}
	r := intersection / denom
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return 0
	}
	return r
}

// Ensure areaRatio stays referenced (it's used by tests).
var _ = areaRatio
