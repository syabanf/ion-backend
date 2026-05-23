package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// FiberStatus mirrors the DB CHECK enum on fiber_links.status.
type FiberStatus string

const (
	FiberStatusOK       FiberStatus = "ok"
	FiberStatusWarn     FiberStatus = "warn"
	FiberStatusCritical FiberStatus = "critical"
	FiberStatusOffline  FiberStatus = "offline"
	FiberStatusUnknown  FiberStatus = "unknown"
)

// fiberOfflineWindow is how long a link can go without a measurement
// before EvaluateAttenuation calls it offline (i.e. the SNMP poll
// dropped the link entirely). Matches the daily fiber tick.
const fiberOfflineWindow = 24 * time.Hour

// FiberLink is one optical span between an OLT port and an ONU. The
// thresholds default to the GPON spec (warn ≥ 25 dBm of measured
// loss; critical ≥ 28 dBm) but admins can override per-row via the
// HTTP surface so per-OLT / per-fiber-type tuning works without a
// code deploy (TC-FAM-004).
//
// Sign convention: see migration docstring. measured_db is the
// absolute magnitude of attenuation, so "higher = worse".
type FiberLink struct {
	ID                  uuid.UUID
	OLTPortID           *uuid.UUID
	ONUSerial           string
	ExpectedLossDB      *float64
	WarnThresholdDB     float64
	CriticalThresholdDB float64
	LastMeasuredDB      *float64
	LastMeasuredAt      *time.Time
	Status              FiberStatus
	CustomerID          *uuid.UUID
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// EvaluateAttenuation classifies a measured Rx loss in dBm magnitude.
// The caller is responsible for stamping LastMeasuredDB +
// LastMeasuredAt + the returned Status onto the link (so this method
// stays pure — easier to test). The "offline" branch fires when the
// last measurement is older than fiberOfflineWindow, regardless of
// the supplied value.
//
// TC-FAM-001/002 acceptance:
//
//	measured <= warn               → ok
//	warn  < measured <= critical   → warn
//	critical < measured            → critical
//	last sample age > 24h          → offline
func (l *FiberLink) EvaluateAttenuation(measuredDB float64, at time.Time) FiberStatus {
	switch {
	case measuredDB > l.CriticalThresholdDB:
		return FiberStatusCritical
	case measuredDB > l.WarnThresholdDB:
		return FiberStatusWarn
	default:
		return FiberStatusOK
	}
}

// IsOffline reports whether the most recent measurement is older
// than fiberOfflineWindow at evaluation time t. The daily cron
// (FiberAttenuationTick) flips offline links so the dashboard count
// doesn't lie. Always false for a link with no measurement yet —
// that surfaces as "unknown" instead.
func (l *FiberLink) IsOffline(now time.Time) bool {
	if l.LastMeasuredAt == nil {
		return false
	}
	return now.Sub(*l.LastMeasuredAt) > fiberOfflineWindow
}

// ValidateThresholds is invoked at create-time so a malformed admin
// payload (critical < warn, negative values) is rejected before
// persistence. The DB CHECK already gates the enum on status, so we
// only need to police the numeric pair here.
func (l *FiberLink) ValidateThresholds() error {
	if l.WarnThresholdDB < 0 || l.CriticalThresholdDB < 0 {
		return errors.Validation("fiber.threshold_negative", "thresholds must be >= 0")
	}
	if l.CriticalThresholdDB < l.WarnThresholdDB {
		return errors.Validation("fiber.threshold_inverted", "critical_threshold_db must be >= warn_threshold_db")
	}
	return nil
}
