// Wave 126 — domain projection of operations.calendar_events.
//
// Calendar entries either originate manually (created_by != nil) or are
// auto-synced from upstream sources (field.maintenance_events,
// operations.bulk_jobs, operations.internal_announcements). Auto-sync
// uses the (event_source, source_id) unique index for idempotency.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// EventKind enumerates the calendar-row kinds. The DB CHECK keeps this
// list in lockstep; new kinds need a migration.
type EventKind string

const (
	EventKindMaintenance      EventKind = "maintenance"
	EventKindHoliday          EventKind = "holiday"
	EventKindTraining         EventKind = "training"
	EventKindBlackout         EventKind = "blackout"
	EventKindAnnouncement     EventKind = "announcement"
	EventKindRelease          EventKind = "release"
	EventKindContractRenewal  EventKind = "contract_renewal"
	EventKindSLADeadline      EventKind = "sla_deadline"
	EventKindCustom           EventKind = "custom"
)

// NormalizeEventKind coerces a string to a valid EventKind; defaults to
// custom.
func NormalizeEventKind(raw string) EventKind {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch EventKind(v) {
	case EventKindMaintenance, EventKindHoliday, EventKindTraining,
		EventKindBlackout, EventKindAnnouncement, EventKindRelease,
		EventKindContractRenewal, EventKindSLADeadline, EventKindCustom:
		return EventKind(v)
	default:
		return EventKindCustom
	}
}

// EventSource enumerates the upstream system the row was synced from.
// 'custom' is the catch-all for manually created entries.
type EventSource string

const (
	SourceFieldMaintenance     EventSource = "field.maintenance"
	SourceOperationsBulkJob    EventSource = "operations.bulk_jobs"
	SourceOperationsAnnounce   EventSource = "operations.announcement"
	SourceEnterpriseInvoicePln EventSource = "enterprise.invoice_plan"
	SourceCustom               EventSource = "custom"
)

// EventScope narrows the audience: global, per-branch, per-team, per-user.
type EventScope string

const (
	ScopeGlobal EventScope = "global"
	ScopeBranch EventScope = "branch"
	ScopeTeam   EventScope = "team"
	ScopeUser   EventScope = "user"
)

// NormalizeScope coerces a string to a valid EventScope. Defaults to global.
func NormalizeScope(raw string) EventScope {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch EventScope(v) {
	case ScopeGlobal, ScopeBranch, ScopeTeam, ScopeUser:
		return EventScope(v)
	default:
		return ScopeGlobal
	}
}

// CalendarEvent is the persistent record.
type CalendarEvent struct {
	ID          uuid.UUID
	EventKind   EventKind
	EventSource EventSource
	SourceID    *uuid.UUID
	Title       string
	Description string
	Scope       EventScope
	ScopeID     *uuid.UUID
	AllDay      bool
	StartsAt    time.Time
	EndsAt      *time.Time
	ColorHex    string
	Metadata    map[string]any
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IsActiveAt returns true when `at` falls within [StartsAt, EndsAt].
// Open-ended events (EndsAt == nil) are active from StartsAt onwards.
// All-day events round to whole-day boundaries.
func (e *CalendarEvent) IsActiveAt(at time.Time) bool {
	if e == nil {
		return false
	}
	start := e.StartsAt
	if e.AllDay {
		start = startOfDay(e.StartsAt)
	}
	if at.Before(start) {
		return false
	}
	if e.EndsAt == nil {
		return true
	}
	end := *e.EndsAt
	if e.AllDay {
		end = startOfDay(*e.EndsAt).Add(24 * time.Hour)
	}
	return at.Before(end) || at.Equal(end)
}

// OverlapsRange returns true when the event's window intersects
// [from, to]. Used by ListInRange's in-memory filter.
func (e *CalendarEvent) OverlapsRange(from, to time.Time) bool {
	if e == nil {
		return false
	}
	if e.StartsAt.After(to) {
		return false
	}
	if e.EndsAt != nil && e.EndsAt.Before(from) {
		return false
	}
	return true
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
