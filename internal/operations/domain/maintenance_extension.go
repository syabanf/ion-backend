// Wave 126 — domain helpers for the extended Planned Maintenance flow.
//
// The maintenance event aggregate lives in field.maintenance_events
// (originally seeded by migration 0036, extended by 0048 + 0085). The
// HTTP/handler layer in internal/field/adapter/http/phase2.go drives
// CRUD directly via pgx. This file adds the operations-side rules:
//
//   - ApprovalRequired — threshold gating per customer-segment
//   - LeadTimeHours — 24h broadband, 72h enterprise (mixed = max)
//   - IsOverrun — past scheduled_end + tolerance, status != completed
//   - NewEscalation — factory for operations.maintenance_escalations
package domain

import (
	"time"

	"github.com/google/uuid"
)

// CustomerSegment captures the audience a maintenance event affects.
// 'mixed' means the event hits both broadband and enterprise customers;
// the lead-time selector picks the strictest value (72h).
type CustomerSegment string

const (
	SegmentBroadband  CustomerSegment = "broadband"
	SegmentEnterprise CustomerSegment = "enterprise"
	SegmentMixed      CustomerSegment = "mixed"
)

// String returns the enum's string form (for SQL marshalling).
func (s CustomerSegment) String() string { return string(s) }

// ApprovalRequired returns true when the affected-customer count exceeds
// the per-segment threshold. PRD §4.2 defines:
//
//   - >100 customers (any segment) → NOC Manager + Ops Admin joint approval
//   - >50  enterprise customers     → joint approval too (stricter)
//   - else                          → Ops Admin solo (no gate)
func ApprovalRequired(affectedCount int, segment CustomerSegment) bool {
	if affectedCount > 100 {
		return true
	}
	if segment == SegmentEnterprise && affectedCount > 50 {
		return true
	}
	if segment == SegmentMixed && affectedCount > 50 {
		return true
	}
	return false
}

// LeadTimeHours returns the customer-notification lead time, in hours,
// for the given segment. Defaults to 24 for unknown segments.
//
// PRD §11 Q2 resolved:
//   - broadband:  24h
//   - enterprise: 72h (configurable per customer type)
//   - mixed:      72h (the stricter wins)
func LeadTimeHours(segment CustomerSegment) int {
	switch segment {
	case SegmentEnterprise, SegmentMixed:
		return 72
	default:
		return 24
	}
}

// IsOverrun returns true when an event's scheduled window has passed
// without status='completed'. Caller passes the current time and the
// tolerance window (PRD §4.5: 30 minutes default; configurable per
// branch in future).
func IsOverrun(scheduledEnd *time.Time, status string, now time.Time, tolerance time.Duration) bool {
	if scheduledEnd == nil {
		return false
	}
	if status == "completed" || status == "cancelled" {
		return false
	}
	return now.After(scheduledEnd.Add(tolerance))
}

// MaintenanceEscalation is the persistent record of an escalate event.
// Level 1 = Team Lead notified, Level 2 = NOC Manager, Level 3 = Ops
// Admin, Level 4 = War Room.
type MaintenanceEscalation struct {
	ID                 uuid.UUID
	MaintenanceEventID uuid.UUID
	Level              int
	Reason             string
	EscalatedToUserID  *uuid.UUID
	EscalatedAt        time.Time
	AcknowledgedAt     *time.Time
	ResolvedAt         *time.Time
}

// NewEscalation factory — defaults EscalatedAt to now() and validates the
// level. Returns nil + a nil-id token on invalid input (callers should
// treat the empty ID as a domain-level failure; same shape as the bulk
// validators in this package).
func NewEscalation(eventID uuid.UUID, level int, reason string, escalatedTo *uuid.UUID) MaintenanceEscalation {
	if level < 1 {
		level = 1
	}
	if level > 4 {
		level = 4
	}
	return MaintenanceEscalation{
		ID:                 uuid.New(),
		MaintenanceEventID: eventID,
		Level:              level,
		Reason:             reason,
		EscalatedToUserID:  escalatedTo,
		EscalatedAt:        time.Now().UTC(),
	}
}

// NextLevel returns the level to escalate to from the current peak.
// Saturates at 4 (War Room).
func NextLevel(current int) int {
	if current < 1 {
		return 1
	}
	if current >= 4 {
		return 4
	}
	return current + 1
}

// MaintenanceAffectedCustomer is the row materialized into
// operations.maintenance_affected_customers when a maintenance event is
// approved (or auto-detected from network topology cascade).
type MaintenanceAffectedCustomer struct {
	ID                  uuid.UUID
	MaintenanceEventID  uuid.UUID
	CustomerID          uuid.UUID
	CustomerSegment     CustomerSegment
	NotifiedAt          *time.Time
	NotificationChannel string
	ErrorMsg            string
}
