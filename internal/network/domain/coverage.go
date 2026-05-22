package domain

import (
	"math"

	"github.com/google/uuid"
)

// CoverageVerdict tells the caller what to do with a coverage check result.
type CoverageVerdict string

const (
	// VerdictCovered — distance ≤ max_cable_run; proceed with onboarding.
	VerdictCovered CoverageVerdict = "covered"
	// VerdictExcess — over the standard limit, but the customer can opt in
	// to pay excess cable cost. The lead can also be marked "Potential".
	VerdictExcess CoverageVerdict = "excess_distance"
	// VerdictUncovered — no ODP within reasonable range.
	VerdictUncovered CoverageVerdict = "uncovered"
)

// CoverageCandidate is one nearby ODP returned in a coverage check.
type CoverageCandidate struct {
	NodeID         uuid.UUID
	NodeName       string
	NodeCode       string
	StraightLineM  float64  // raw great-circle distance
	CableDistanceM float64  // straight-line × route_factor
	ExcessM        float64  // 0 when within limit
	ExcessCharge   float64  // 0 when within limit
	AvailablePorts int
	GPSLat         *float64 // node's GPS — nil when polygon-only match
	GPSLng         *float64
	InPolygon      bool // true if the query point fell inside this node's polygon
}

// CoverageResult is what /api/network/coverage/check returns.
type CoverageResult struct {
	Verdict          CoverageVerdict
	BestCandidate    *CoverageCandidate  // nil when Uncovered
	OtherCandidates  []CoverageCandidate // up to N nearest, for UX
	MaxCableRunM     int
	CableRouteFactor float64
	ExcessPricePerM  float64
}

// GeoJSONPolygon is the wire shape we accept for polygon writes and emit
// for polygon reads. Matches RFC 7946 Polygon — first ring outer, rest holes.
// Coordinates are [lng, lat] pairs (GeoJSON's order, opposite of "lat,lng").
type GeoJSONPolygon struct {
	Type        string        `json:"type"`        // always "Polygon"
	Coordinates [][][]float64 `json:"coordinates"` // [ring][point][lng,lat]
}

// IsValid does the absolute minimum sanity check — caller relies on
// PostGIS's ST_IsValid for the real geometric check at insert time.
func (p GeoJSONPolygon) IsValid() bool {
	if p.Type != "Polygon" {
		return false
	}
	if len(p.Coordinates) == 0 {
		return false
	}
	for _, ring := range p.Coordinates {
		if len(ring) < 4 {
			return false
		}
		first := ring[0]
		last := ring[len(ring)-1]
		if len(first) < 2 || len(last) < 2 {
			return false
		}
		if first[0] != last[0] || first[1] != last[1] {
			return false
		}
	}
	return true
}

// HaversineMeters returns great-circle distance in meters between two
// (lat, lng) points. Earth treated as a sphere — accuracy is ±0.5% which
// is well within "is this address within 210m of an ODP" tolerance.
func HaversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0 // mean earth radius in meters
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}
