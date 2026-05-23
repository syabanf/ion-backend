// Package domain models the NOC monitoring bounded context — service
// probes, fiber link signal, fault events, impact links, topology
// snapshots. Pure business types; no transport or storage imports.
//
// Wave 112 scope (TC-NSM / TC-NFA / TC-NFI / TC-NTV / TC-NAW):
// per the spec these are the in-process equivalents of what an NMS
// (Network Management System) would normally do — the probe runners
// are stubbed for now, the topology builder reads from another
// bounded context via a port (decoupled), and the fault state
// machine drives the NOC dashboard.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ProbeKind enumerates the supported probe types. The DB CHECK
// constraint mirrors this set; new kinds need a migration + a
// matching ProbeRunner stub in adapter/probes.
type ProbeKind string

const (
	ProbeKindRTT        ProbeKind = "rtt"
	ProbeKindPacketLoss ProbeKind = "packet_loss"
	ProbeKindThroughput ProbeKind = "throughput"
	ProbeKindSpeedtest  ProbeKind = "speedtest"
	ProbeKindOLTSignal  ProbeKind = "olt_signal"
)

func (k ProbeKind) Valid() bool {
	switch k {
	case ProbeKindRTT, ProbeKindPacketLoss, ProbeKindThroughput, ProbeKindSpeedtest, ProbeKindOLTSignal:
		return true
	}
	return false
}

// SampleStatus mirrors the DB CHECK enum on service_health_samples
// AND the denormalized last_status on service_probes. Same set is
// reused on fiber link evaluation so the dashboard chip rendering
// is uniform.
type SampleStatus string

const (
	SampleStatusOK          SampleStatus = "ok"
	SampleStatusWarn        SampleStatus = "warn"
	SampleStatusCritical    SampleStatus = "critical"
	SampleStatusUnreachable SampleStatus = "unreachable"
	SampleStatusUnknown     SampleStatus = "unknown"
)

// ServiceProbe is the per-customer probe definition. The cron runner
// uses (is_active, last_probed_at + interval_seconds) to decide
// which probes are due on each tick. threshold_warn / threshold_critical
// are compared by Evaluate(); for kinds where "higher is worse" (RTT,
// packet loss, attenuation) the sense is straightforward, for
// "throughput" the comparison is inverted by the runner before passing
// the value here so the threshold semantics stay uniform.
type ServiceProbe struct {
	ID                uuid.UUID
	CustomerID        uuid.UUID
	PlanID            *uuid.UUID
	ProbeKind         ProbeKind
	ProbeTarget       string
	IntervalSeconds   int
	ThresholdWarn     *float64
	ThresholdCritical *float64
	IsActive          bool
	LastProbedAt      *time.Time
	LastStatus        SampleStatus
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewServiceProbe constructs an active probe. Validation matches the
// DB CHECK constraint shape; runtime arithmetic on thresholds
// (warn < critical for the "higher is worse" kinds) lives in
// Evaluate() so a misconfigured pair degrades to "ok" rather than
// failing the create.
func NewServiceProbe(
	customerID uuid.UUID,
	kind ProbeKind,
	target string,
	intervalSeconds int,
	warn, critical *float64,
) (*ServiceProbe, error) {
	if customerID == uuid.Nil {
		return nil, errors.Validation("probe.customer_required", "customer_id is required")
	}
	if !kind.Valid() {
		return nil, errors.Validation("probe.kind_invalid", "probe_kind is not one of rtt/packet_loss/throughput/speedtest/olt_signal")
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 60
	}
	now := time.Now().UTC()
	return &ServiceProbe{
		ID:                uuid.New(),
		CustomerID:        customerID,
		ProbeKind:         kind,
		ProbeTarget:       strings.TrimSpace(target),
		IntervalSeconds:   intervalSeconds,
		ThresholdWarn:     warn,
		ThresholdCritical: critical,
		IsActive:          true,
		LastStatus:        SampleStatusUnknown,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// Evaluate classifies a probe reading into a SampleStatus by comparing
// against the configured thresholds. The semantics:
//
//   - For "higher is worse" kinds (rtt, packet_loss, olt_signal where
//     we store positive loss magnitude): value > critical → critical;
//     value > warn → warn; otherwise ok.
//
//   - For "lower is worse" kinds (throughput): the runner inverts
//     before passing in, so the comparison stays the same. (E.g. for
//     a 100 Mbps plan with an 80/50 floor, the runner passes
//     100 - measured; if measured = 30 → value = 70 → > 50 → critical.)
//
// A nil threshold means "no opinion at this tier"; if BOTH thresholds
// are nil the probe degrades to SampleStatusOK (informational only).
func (p *ServiceProbe) Evaluate(value float64) SampleStatus {
	// Critical takes precedence over warn.
	if p.ThresholdCritical != nil && value > *p.ThresholdCritical {
		return SampleStatusCritical
	}
	if p.ThresholdWarn != nil && value > *p.ThresholdWarn {
		return SampleStatusWarn
	}
	return SampleStatusOK
}

// IsStale reports whether the probe is due for a new sample at time t.
// The cron tick uses this to find the working set. A probe with no
// last_probed_at (never run) is always stale.
func (p *ServiceProbe) IsStale(now time.Time) bool {
	if !p.IsActive {
		return false
	}
	if p.LastProbedAt == nil {
		return true
	}
	due := p.LastProbedAt.Add(time.Duration(p.IntervalSeconds) * time.Second)
	return !now.Before(due)
}

// Deactivate flips is_active off. Idempotent.
func (p *ServiceProbe) Deactivate(at time.Time) {
	if !p.IsActive {
		return
	}
	p.IsActive = false
	p.UpdatedAt = at.UTC()
}
