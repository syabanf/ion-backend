package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// FaultKind mirrors the DB CHECK enum.
type FaultKind string

const (
	FaultKindDeviceDown       FaultKind = "device_down"
	FaultKindFiberDegradation FaultKind = "fiber_degradation"
	FaultKindProbeCritical    FaultKind = "probe_critical"
	FaultKindNOCAlert         FaultKind = "noc_alert"
	FaultKindOLTPortFlap      FaultKind = "olt_port_flap"
	FaultKindManualOutage     FaultKind = "manual_outage"
)

func (k FaultKind) Valid() bool {
	switch k {
	case FaultKindDeviceDown, FaultKindFiberDegradation, FaultKindProbeCritical,
		FaultKindNOCAlert, FaultKindOLTPortFlap, FaultKindManualOutage:
		return true
	}
	return false
}

// FaultSeverity mirrors the DB CHECK enum.
type FaultSeverity string

const (
	FaultSeverityLow      FaultSeverity = "low"
	FaultSeverityMedium   FaultSeverity = "medium"
	FaultSeverityHigh     FaultSeverity = "high"
	FaultSeverityCritical FaultSeverity = "critical"
)

func (s FaultSeverity) Valid() bool {
	switch s {
	case FaultSeverityLow, FaultSeverityMedium, FaultSeverityHigh, FaultSeverityCritical:
		return true
	}
	return false
}

// FaultStatus mirrors the DB CHECK enum.
//
// State machine (enforced by the methods below):
//
//	open → acknowledged → investigating → mitigated → resolved (terminal)
//	open → duplicate (terminal; secondary terminal exit)
//
// AlertWOService.ConvertFaultToWO promotes open → investigating
// (skipping acknowledged) when the NOC creates a WO directly off an
// alert — the WO acts as both the acknowledgement and the start of
// hands-on work.
type FaultStatus string

const (
	FaultStatusOpen          FaultStatus = "open"
	FaultStatusAcknowledged  FaultStatus = "acknowledged"
	FaultStatusInvestigating FaultStatus = "investigating"
	FaultStatusMitigated     FaultStatus = "mitigated"
	FaultStatusResolved      FaultStatus = "resolved"
	FaultStatusDuplicate     FaultStatus = "duplicate"
)

// FaultEvent is the incident header. (source_id, source_kind) is the
// polymorphic pointer to whatever raised the fault — see the migration
// docstring for the kind set. customer_impact_count is denormalized
// by LinkImpact on the usecase service.
type FaultEvent struct {
	ID                  uuid.UUID
	Kind                FaultKind
	Severity            FaultSeverity
	SourceID            *uuid.UUID
	SourceKind          string
	StartedAt           time.Time
	DetectedAt          time.Time
	AcknowledgedAt      *time.Time
	AcknowledgedBy      *uuid.UUID
	ResolvedAt          *time.Time
	ResolvedBy          *uuid.UUID
	RootCause           string
	CustomerImpactCount int
	Status              FaultStatus
	TicketWOID          *uuid.UUID
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// NewFaultEvent constructs an open fault. The kind + severity are
// validated against the DB CHECK enums; the caller is responsible
// for setting source_id + source_kind if known (cron-emitted faults
// always carry both; manual NOC entries leave them nil).
func NewFaultEvent(kind FaultKind, severity FaultSeverity, sourceID *uuid.UUID, sourceKind string) (*FaultEvent, error) {
	if !kind.Valid() {
		return nil, errors.Validation("fault.kind_invalid", "kind must be one of device_down/fiber_degradation/probe_critical/noc_alert/olt_port_flap/manual_outage")
	}
	if !severity.Valid() {
		return nil, errors.Validation("fault.severity_invalid", "severity must be one of low/medium/high/critical")
	}
	now := time.Now().UTC()
	return &FaultEvent{
		ID:         uuid.New(),
		Kind:       kind,
		Severity:   severity,
		SourceID:   sourceID,
		SourceKind: strings.TrimSpace(sourceKind),
		StartedAt:  now,
		DetectedAt: now,
		Status:     FaultStatusOpen,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Acknowledge moves open → acknowledged. Idempotent on already-
// acknowledged. Refuses any terminal state (resolved, duplicate) and
// any post-acknowledged state (the usecase should call Investigate
// directly instead).
func (f *FaultEvent) Acknowledge(by uuid.UUID, at time.Time) error {
	switch f.Status {
	case FaultStatusAcknowledged:
		return nil
	case FaultStatusOpen:
		atUTC := at.UTC()
		f.Status = FaultStatusAcknowledged
		f.AcknowledgedAt = &atUTC
		f.AcknowledgedBy = &by
		f.UpdatedAt = atUTC
		return nil
	default:
		return errors.Conflict("fault.cannot_acknowledge", "fault is past the acknowledge step")
	}
}

// Investigate moves open|acknowledged → investigating. Idempotent on
// already-investigating. Used by both the NOC dashboard
// "Start Investigation" button and by ConvertFaultToWO (which leaps
// open → investigating directly).
func (f *FaultEvent) Investigate(by uuid.UUID, at time.Time) error {
	switch f.Status {
	case FaultStatusInvestigating:
		return nil
	case FaultStatusOpen, FaultStatusAcknowledged:
		atUTC := at.UTC()
		f.Status = FaultStatusInvestigating
		if f.AcknowledgedAt == nil {
			// Stamp the acknowledged_at lazily so the audit trail
			// always shows who took ownership, even on the open →
			// investigating shortcut.
			f.AcknowledgedAt = &atUTC
			f.AcknowledgedBy = &by
		}
		f.UpdatedAt = atUTC
		return nil
	default:
		return errors.Conflict("fault.cannot_investigate", "fault is in a terminal or post-investigation state")
	}
}

// Mitigate moves investigating → mitigated. Captures the root_cause
// note. Idempotent on already-mitigated. Refuses anything before
// investigating (the usecase should drive through Acknowledge +
// Investigate first).
func (f *FaultEvent) Mitigate(by uuid.UUID, at time.Time, rootCause string) error {
	rootCause = strings.TrimSpace(rootCause)
	switch f.Status {
	case FaultStatusMitigated:
		if rootCause != "" {
			f.RootCause = rootCause
		}
		return nil
	case FaultStatusInvestigating:
		atUTC := at.UTC()
		f.Status = FaultStatusMitigated
		f.RootCause = rootCause
		f.UpdatedAt = atUTC
		return nil
	default:
		return errors.Conflict("fault.cannot_mitigate", "fault must be in investigating before mitigation")
	}
}

// Resolve moves mitigated → resolved (terminal). Also accepts a
// direct investigating → resolved shortcut for cases where there's
// no separate mitigation step (e.g. a false-positive close-out).
// Idempotent on already-resolved.
func (f *FaultEvent) Resolve(by uuid.UUID, at time.Time) error {
	switch f.Status {
	case FaultStatusResolved:
		return nil
	case FaultStatusMitigated, FaultStatusInvestigating, FaultStatusAcknowledged:
		atUTC := at.UTC()
		f.Status = FaultStatusResolved
		f.ResolvedAt = &atUTC
		f.ResolvedBy = &by
		f.UpdatedAt = atUTC
		return nil
	default:
		return errors.Conflict("fault.cannot_resolve", "fault is open or in a terminal state")
	}
}

// MarkDuplicate moves open → duplicate (terminal). Used by the NOC
// dashboard's "merge with existing incident" action; the of-ID
// parameter is stamped onto root_cause for forensic linkage.
func (f *FaultEvent) MarkDuplicate(of uuid.UUID, at time.Time) error {
	switch f.Status {
	case FaultStatusDuplicate:
		return nil
	case FaultStatusOpen:
		atUTC := at.UTC()
		f.Status = FaultStatusDuplicate
		f.RootCause = "duplicate of " + of.String()
		f.UpdatedAt = atUTC
		return nil
	default:
		return errors.Conflict("fault.cannot_duplicate", "duplicate is only allowed from open")
	}
}
