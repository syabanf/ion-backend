// Wave 126 — domain types for Internal Announcements.
//
// The persisted row lives in operations.internal_announcements (created
// by migration 0048, severity-realigned by 0085). This file gives the
// dispatcher state machine + the recipient row plus the severity enum
// that matches the PRD (info|important|urgent — not the legacy
// info|warning|critical the database used to enforce).
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// AnnouncementSeverity enumerates the PRD-correct severities. The
// legacy values (warning/critical) are migrated by 0085's UPDATE.
type AnnouncementSeverity string

const (
	AnnouncementInfo      AnnouncementSeverity = "info"
	AnnouncementImportant AnnouncementSeverity = "important"
	AnnouncementUrgent    AnnouncementSeverity = "urgent"
)

// NormalizeSeverity coerces any legacy or freeform value to a valid
// AnnouncementSeverity. Unknown strings default to info.
func NormalizeSeverity(raw string) AnnouncementSeverity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "important", "warning":
		return AnnouncementImportant
	case "urgent", "critical":
		return AnnouncementUrgent
	case "info", "":
		return AnnouncementInfo
	default:
		return AnnouncementInfo
	}
}

// AnnouncementTargetAudience is the bulk-targeting label. Per-user
// targeting still uses the existing `targeting` JSONB column for
// backwards compat.
type AnnouncementTargetAudience string

const (
	AudienceAll          AnnouncementTargetAudience = "all"
	AudienceAgents       AnnouncementTargetAudience = "agents"
	AudienceSupervisors  AnnouncementTargetAudience = "supervisors"
	AudienceTechnicians  AnnouncementTargetAudience = "technicians"
	AudienceCustomers    AnnouncementTargetAudience = "customers"
)

// NormalizeAudience coerces any freeform value to a valid audience.
func NormalizeAudience(raw string) AnnouncementTargetAudience {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "agents":
		return AudienceAgents
	case "supervisors":
		return AudienceSupervisors
	case "technicians":
		return AudienceTechnicians
	case "customers":
		return AudienceCustomers
	default:
		return AudienceAll
	}
}

// AnnouncementDispatchStatus is the state machine for the dispatcher
// cron. State transitions:
//
//	pending -> dispatching -> dispatched | failed | partial
//
// `partial` lands when at least one recipient delivered but some failed
// — the row is not retried automatically.
type AnnouncementDispatchStatus string

const (
	DispatchPending     AnnouncementDispatchStatus = "pending"
	DispatchDispatching AnnouncementDispatchStatus = "dispatching"
	DispatchDispatched  AnnouncementDispatchStatus = "dispatched"
	DispatchFailed      AnnouncementDispatchStatus = "failed"
	DispatchPartial     AnnouncementDispatchStatus = "partial"
)

// Announcement is the domain projection of operations.internal_announcements.
type Announcement struct {
	ID              uuid.UUID
	Title           string
	Body            string
	Severity        AnnouncementSeverity
	TargetAudience  AnnouncementTargetAudience
	Channels        []string // copy of the channels JSONB (push/email/wa/sms)
	ScheduledAt     *time.Time
	ExpiresAt       *time.Time
	SentAt          *time.Time
	DispatchedAt    *time.Time
	DispatchStatus  AnnouncementDispatchStatus
	SentCount       int
	CreatedBy       *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// MarkDispatching transitions pending -> dispatching. Idempotent: a row
// already in dispatching stays put.
func (a *Announcement) MarkDispatching(now time.Time) {
	if a == nil {
		return
	}
	if a.DispatchStatus == DispatchPending {
		a.DispatchStatus = DispatchDispatching
		a.UpdatedAt = now
	}
}

// MarkAllRecipientsDelivered flips the status to dispatched + stamps the
// timestamps. Caller passes the now() to keep the call deterministic in
// tests.
func (a *Announcement) MarkAllRecipientsDelivered(now time.Time, totalRecipients int) {
	if a == nil {
		return
	}
	a.DispatchStatus = DispatchDispatched
	a.SentCount = totalRecipients
	a.DispatchedAt = &now
	if a.SentAt == nil {
		a.SentAt = &now
	}
	a.UpdatedAt = now
}

// MarkSomeFailed flips to partial when some succeeded + some failed, or
// failed when nobody got it. Caller passes the breakdown.
func (a *Announcement) MarkSomeFailed(now time.Time, deliveredCount, totalRecipients int) {
	if a == nil {
		return
	}
	switch {
	case totalRecipients <= 0:
		a.DispatchStatus = DispatchFailed
	case deliveredCount == 0:
		a.DispatchStatus = DispatchFailed
	case deliveredCount < totalRecipients:
		a.DispatchStatus = DispatchPartial
	default:
		a.DispatchStatus = DispatchDispatched
	}
	a.SentCount = deliveredCount
	a.DispatchedAt = &now
	if a.SentAt == nil {
		a.SentAt = &now
	}
	a.UpdatedAt = now
}

// AnnouncementRecipient is the per-user delivery record. Mirrors
// operations.announcement_recipients.
type AnnouncementRecipient struct {
	ID             uuid.UUID
	AnnouncementID uuid.UUID
	UserID         uuid.UUID
	DeliveredAt    *time.Time
	ReadAt         *time.Time
	Channel        string
	ErrorMsg       string
}

// MarkDelivered stamps the row as delivered via the given channel.
func (r *AnnouncementRecipient) MarkDelivered(now time.Time, channel string) {
	if r == nil {
		return
	}
	r.DeliveredAt = &now
	r.Channel = channel
}

// MarkRead stamps the read timestamp. Idempotent.
func (r *AnnouncementRecipient) MarkRead(now time.Time) {
	if r == nil {
		return
	}
	if r.ReadAt == nil {
		r.ReadAt = &now
	}
}
