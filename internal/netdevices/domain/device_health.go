package domain

import (
	"time"

	"github.com/google/uuid"
)

// HealthSnapshot is a single telemetry reading. The polling pipeline
// inserts one per polling cycle per device; ComputeHealthScore collapses
// it into a 0–100 score used by the degradation watcher.
type HealthSnapshot struct {
	ID            uuid.UUID
	DeviceID      uuid.UUID
	SnappedAt     time.Time
	UptimeSeconds *int64
	SignalDBM     *float64 // PON signal level for ONT, RSSI for AP
	PacketLossPct *float64
	CPUPct        *float64
	MemoryPct     *float64
	RawPayload    []byte // vendor blob, opaque
}

// NewHealthSnapshot is a lightweight factory — no domain invariants on
// telemetry; the constructor exists so callers don't accidentally
// forget to stamp the ID + timestamp.
func NewHealthSnapshot(deviceID uuid.UUID, at time.Time) *HealthSnapshot {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return &HealthSnapshot{
		ID:        uuid.New(),
		DeviceID:  deviceID,
		SnappedAt: at,
	}
}

// ComputeHealthScore reduces a snapshot to a 0..100 score.
//
// Scoring philosophy: 100 is a perfectly healthy device with no
// concerning readings; each degraded reading subtracts a fixed penalty.
// nil fields contribute zero — we don't penalise missing telemetry,
// only bad telemetry.
//
//   - signal_dbm: ONT/AP signal level. -25 dBm or stronger is full
//     marks; below -28 is "marginal", below -30 is "bad".
//   - packet_loss_pct: 0–1% is full marks; 5%+ is bad.
//   - cpu_pct + memory_pct: <80% is full marks; >95% is bad.
//
// The exact thresholds are intentionally coarse — real fleet-tuned
// scoring lives in the NOC analytics pipeline (Wave 112). This function
// is the in-process "good enough" used by the degradation watcher.
func ComputeHealthScore(s HealthSnapshot) int {
	score := 100

	if s.SignalDBM != nil {
		v := *s.SignalDBM
		switch {
		case v <= -30: // very bad
			score -= 30
		case v <= -28: // marginal
			score -= 15
		}
	}
	if s.PacketLossPct != nil {
		v := *s.PacketLossPct
		switch {
		case v >= 5:
			score -= 30
		case v >= 1:
			score -= 10
		}
	}
	if s.CPUPct != nil {
		v := *s.CPUPct
		switch {
		case v >= 95:
			score -= 15
		case v >= 80:
			score -= 5
		}
	}
	if s.MemoryPct != nil {
		v := *s.MemoryPct
		switch {
		case v >= 95:
			score -= 15
		case v >= 80:
			score -= 5
		}
	}
	if score < 0 {
		score = 0
	}
	return score
}
