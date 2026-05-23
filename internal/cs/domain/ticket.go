// Package domain holds the customer-service bounded-context entities.
//
// Hexagonal conventions (same as identity / crm / reseller / hris):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map
//     them to the right HTTP status without inspecting strings.
//
// Wave 123 scope: Ticket aggregate + 7-state state machine, channel
// taxonomy, comment + mention sub-entities. Wave 124 will extend the
// Ticket struct with SLA-template snapshot + breach flag.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Enums
// =====================================================================

// TicketStatus enumerates the 7 lifecycle states. Distinct from
// field.tickets.status (5 states) — the catalog requires `assigned`
// (between open and in_progress) and `pending_internal` (sibling of
// pending_customer).
type TicketStatus string

const (
	TicketStatusOpen             TicketStatus = "open"
	TicketStatusAssigned         TicketStatus = "assigned"
	TicketStatusInProgress       TicketStatus = "in_progress"
	TicketStatusPendingCustomer  TicketStatus = "pending_customer"
	TicketStatusPendingInternal  TicketStatus = "pending_internal"
	TicketStatusResolved         TicketStatus = "resolved"
	TicketStatusClosed           TicketStatus = "closed"
)

// TicketType is the workflow-oriented taxonomy. Distinct from
// field.tickets.category (symptom-oriented: no_internet / slow_speed /
// etc.). The two stores remain divergent.
type TicketType string

const (
	TicketTypeTechnical      TicketType = "technical"
	TicketTypeBilling        TicketType = "billing"
	TicketTypeComplaint      TicketType = "complaint"
	TicketTypeServiceRequest TicketType = "service_request"
	TicketTypeInformation    TicketType = "information"
)

// Priority controls queue ordering. Wave 124 will use (customer_type,
// ticket_type, priority) as the SLA matrix lookup key.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// OpenedVia mirrors the cs.tickets.opened_via CHECK enum. The
// cs.ticket_channels table holds display metadata for the same set.
type OpenedVia string

const (
	OpenedViaPortal        OpenedVia = "portal"
	OpenedViaWhatsApp      OpenedVia = "whatsapp"
	OpenedViaPhone         OpenedVia = "phone"
	OpenedViaEmail         OpenedVia = "email"
	OpenedViaWalkin        OpenedVia = "walkin"
	OpenedViaAgentInternal OpenedVia = "agent_internal"
	OpenedViaAPI           OpenedVia = "api"
	OpenedViaTechApp       OpenedVia = "tech_app"
)

// PauseKind disambiguates the two pending states from the usecase
// layer. The state machine maps pause(WaitCustomer) → PendingCustomer.
type PauseKind string

const (
	PauseWaitCustomer PauseKind = "customer"
	PauseWaitInternal PauseKind = "internal"
)

// =====================================================================
// Ticket aggregate
// =====================================================================

// Ticket is the CS aggregate. Mutating methods enforce the state
// machine + housekeeping (pause accumulator, first-response timestamp).
//
// Wave 124 added the SLA-snapshot + breach fields. The matrix lookup
// happens in usecase.SLAService.ApplyOnCreate; the values are stamped
// onto the ticket via ApplySLA and never recomputed after the ticket
// is closed.
type Ticket struct {
	ID                 uuid.UUID
	TicketNo           string
	CustomerID         uuid.UUID
	OpenedBy           uuid.UUID
	OpenedVia          OpenedVia
	TicketType         TicketType
	Title              string
	Description        string
	Status             TicketStatus
	Priority           Priority
	AssignedUserID     *uuid.UUID
	AssignedTeamID     *uuid.UUID
	FirstResponseAt    *time.Time
	ResolvedAt         *time.Time
	ClosedAt           *time.Time
	EscalatedAt        *time.Time
	EscalationLevel    int
	RelatedWOID        *uuid.UUID
	RelatedInvoiceID   *uuid.UUID
	PauseSeconds       int64
	PausedSince        *time.Time
	SourceMetadata     map[string]any
	CreatedAt          time.Time
	UpdatedAt          time.Time

	// Wave 124 — SLA snapshot. SLAMatrixID is a soft FK (nullable so
	// pre-Wave-124 tickets continue to load); the due_at timestamps are
	// the authoritative SLA targets. breach flags are flipped by the
	// cron evaluator; warned_at records when we crossed the warn line
	// so we don't dispatch duplicate breach-warn notifications.
	SLAMatrixID              *uuid.UUID
	SLAFirstResponseDueAt    *time.Time
	SLAResolveDueAt          *time.Time
	SLABreachedFirstResponse bool
	SLABreachedResolve       bool
	SLAWarnedAt              *time.Time
}

// NewTicket constructs a fresh ticket in `open` status.
//
// The ticket_no is generated by the usecase layer (date + sequence),
// not the domain — that lets the postgres adapter use a query for the
// next sequence number without bringing DB knowledge into the domain.
func NewTicket(
	customerID, openedBy uuid.UUID,
	openedVia OpenedVia,
	ticketType TicketType,
	title, description string,
	priority Priority,
) (*Ticket, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.Validation("cs.ticket.title_required", "title is required")
	}
	if customerID == uuid.Nil {
		return nil, errors.Validation("cs.ticket.customer_required", "customer_id is required")
	}
	if openedBy == uuid.Nil {
		return nil, errors.Validation("cs.ticket.opened_by_required", "opened_by is required")
	}
	if !openedVia.Valid() {
		return nil, errors.Validation("cs.ticket.opened_via_invalid", "opened_via is not a recognized channel")
	}
	if !ticketType.Valid() {
		return nil, errors.Validation("cs.ticket.type_invalid", "ticket_type is not recognized")
	}
	if priority == "" {
		priority = PriorityNormal
	}
	if !priority.Valid() {
		return nil, errors.Validation("cs.ticket.priority_invalid", "priority is not recognized")
	}
	now := time.Now().UTC()
	return &Ticket{
		ID:          uuid.New(),
		CustomerID:  customerID,
		OpenedBy:    openedBy,
		OpenedVia:   openedVia,
		TicketType:  ticketType,
		Title:       title,
		Description: strings.TrimSpace(description),
		Status:      TicketStatusOpen,
		Priority:    priority,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// =====================================================================
// State machine
//
//   open → assigned (Assign)
//   open|assigned → in_progress (Start)
//   in_progress → pending_customer | pending_internal (Pause)
//   pending_customer|pending_internal → in_progress (Resume)
//   in_progress → resolved (Resolve)
//   resolved → closed (Close — terminal)
//   resolved|closed → in_progress (Reopen)
//
// MarkFirstResponse is idempotent. AddPauseDuration accumulates
// pause_seconds — used by SLA calc in Wave 124.
// =====================================================================

// Assign moves open → assigned and records the assignee.
func (t *Ticket) Assign(assignedUserID uuid.UUID) error {
	if assignedUserID == uuid.Nil {
		return errors.Validation("cs.ticket.assignee_required", "assigned_user_id is required")
	}
	switch t.Status {
	case TicketStatusOpen:
		// fall through
	case TicketStatusAssigned:
		// idempotent re-assign — allow only if assignee changes
		if t.AssignedUserID != nil && *t.AssignedUserID == assignedUserID {
			return nil
		}
	default:
		return errors.Conflict("cs.ticket.cannot_assign",
			"ticket must be open or assigned to be (re)assigned, got "+string(t.Status))
	}
	t.Status = TicketStatusAssigned
	t.AssignedUserID = &assignedUserID
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// Start moves open|assigned → in_progress and stamps first_response_at
// on the first transition (idempotent on repeat Starts).
func (t *Ticket) Start(byUserID uuid.UUID) error {
	switch t.Status {
	case TicketStatusOpen, TicketStatusAssigned:
		// ok
	case TicketStatusInProgress:
		// idempotent on already-in-progress
		return nil
	default:
		return errors.Conflict("cs.ticket.cannot_start",
			"ticket must be open or assigned to start work, got "+string(t.Status))
	}
	now := time.Now().UTC()
	t.Status = TicketStatusInProgress
	if t.AssignedUserID == nil {
		// self-assign on start when no explicit assignee yet
		t.AssignedUserID = &byUserID
	}
	t.MarkFirstResponse(now)
	t.UpdatedAt = now
	return nil
}

// Pause moves in_progress → pending_*; stamps paused_since.
func (t *Ticket) Pause(kind PauseKind, _ string) error {
	if t.Status != TicketStatusInProgress {
		return errors.Conflict("cs.ticket.cannot_pause",
			"ticket must be in_progress to pause, got "+string(t.Status))
	}
	switch kind {
	case PauseWaitCustomer:
		t.Status = TicketStatusPendingCustomer
	case PauseWaitInternal:
		t.Status = TicketStatusPendingInternal
	default:
		return errors.Validation("cs.ticket.pause_kind_invalid", "pause kind must be customer or internal")
	}
	now := time.Now().UTC()
	t.PausedSince = &now
	t.UpdatedAt = now
	return nil
}

// Resume moves pending_* → in_progress and accumulates pause_seconds.
func (t *Ticket) Resume(_ uuid.UUID) error {
	switch t.Status {
	case TicketStatusPendingCustomer, TicketStatusPendingInternal:
		// ok
	default:
		return errors.Conflict("cs.ticket.cannot_resume",
			"ticket must be pending_customer or pending_internal to resume, got "+string(t.Status))
	}
	now := time.Now().UTC()
	if t.PausedSince != nil {
		t.AddPauseDuration(int64(now.Sub(*t.PausedSince).Seconds()))
		t.PausedSince = nil
	}
	t.Status = TicketStatusInProgress
	t.UpdatedAt = now
	return nil
}

// Resolve moves in_progress → resolved. Resolution text is required so
// the close-event payload can carry it for CSAT request.
func (t *Ticket) Resolve(_ uuid.UUID, resolution string) error {
	if t.Status != TicketStatusInProgress {
		return errors.Conflict("cs.ticket.cannot_resolve",
			"ticket must be in_progress to resolve, got "+string(t.Status))
	}
	if strings.TrimSpace(resolution) == "" {
		return errors.Validation("cs.ticket.resolution_required", "resolution notes are required")
	}
	now := time.Now().UTC()
	t.Status = TicketStatusResolved
	t.ResolvedAt = &now
	t.UpdatedAt = now
	return nil
}

// Close moves resolved → closed. Terminal — Reopen() is the only way
// out.
func (t *Ticket) Close(_ uuid.UUID) error {
	switch t.Status {
	case TicketStatusResolved:
		// ok
	case TicketStatusClosed:
		// idempotent
		return nil
	default:
		return errors.Conflict("cs.ticket.cannot_close",
			"ticket must be resolved to close, got "+string(t.Status))
	}
	now := time.Now().UTC()
	t.Status = TicketStatusClosed
	t.ClosedAt = &now
	t.UpdatedAt = now
	return nil
}

// Reopen moves resolved|closed → in_progress. Resets the resolution
// timestamps so the SLA timer starts ticking again, increments
// escalation_level (Wave 124 will use this for max-reopen escalation).
func (t *Ticket) Reopen(_ uuid.UUID, _ string) error {
	switch t.Status {
	case TicketStatusResolved, TicketStatusClosed:
		// ok
	default:
		return errors.Conflict("cs.ticket.cannot_reopen",
			"ticket must be resolved or closed to reopen, got "+string(t.Status))
	}
	now := time.Now().UTC()
	t.Status = TicketStatusInProgress
	t.ResolvedAt = nil
	t.ClosedAt = nil
	t.EscalationLevel++
	t.UpdatedAt = now
	return nil
}

// ChangePriority updates priority on any non-closed ticket. Refuses
// closed tickets — admin would have to reopen first.
func (t *Ticket) ChangePriority(newPriority Priority) error {
	if !newPriority.Valid() {
		return errors.Validation("cs.ticket.priority_invalid", "priority is not recognized")
	}
	if t.Status == TicketStatusClosed {
		return errors.Conflict("cs.ticket.priority_locked",
			"priority cannot change on a closed ticket")
	}
	if t.Priority == newPriority {
		return nil
	}
	t.Priority = newPriority
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkFirstResponse stamps first_response_at if not already set.
// Called by Start; safe to call from other paths (e.g. agent-initiated
// comment before status change in a future wave).
func (t *Ticket) MarkFirstResponse(at time.Time) {
	if t.FirstResponseAt != nil {
		return
	}
	v := at.UTC()
	t.FirstResponseAt = &v
}

// AddPauseDuration accumulates pause_seconds. Used by Resume and by
// the future SLA evaluator if it needs to seal an in-flight pause
// before computing SLA-due.
func (t *Ticket) AddPauseDuration(seconds int64) {
	if seconds < 0 {
		return
	}
	t.PauseSeconds += seconds
}

// EffectiveAge returns wall-clock age minus paused time. Used by the
// SLA evaluator (Wave 124). If currently paused, the live in-flight
// pause window is also subtracted.
func (t *Ticket) EffectiveAge(now time.Time) time.Duration {
	wall := now.Sub(t.CreatedAt)
	paused := time.Duration(t.PauseSeconds) * time.Second
	if t.PausedSince != nil {
		paused += now.Sub(*t.PausedSince)
	}
	if paused < 0 {
		paused = 0
	}
	d := wall - paused
	if d < 0 {
		return 0
	}
	return d
}

// IsTerminal reports whether the ticket can no longer be edited
// without a Reopen.
func (t *Ticket) IsTerminal() bool {
	return t.Status == TicketStatusClosed
}

// =====================================================================
// SLA snapshot helpers (Wave 124)
//
// ApplySLA is idempotent — calling it twice with the same matrix id
// just refreshes the due-date snapshot. The breach flags + warned_at
// are NOT cleared by ApplySLA; only the cron evaluator (which sets
// them) is allowed to reset them.
// =====================================================================

// ApplySLA stamps the SLA-matrix snapshot onto the ticket. Called by
// SLAService at ticket open + after priority / type changes.
func (t *Ticket) ApplySLA(matrixID uuid.UUID, firstResponseDueAt, resolveDueAt time.Time) {
	if matrixID == uuid.Nil {
		return
	}
	id := matrixID
	fr := firstResponseDueAt.UTC()
	rv := resolveDueAt.UTC()
	t.SLAMatrixID = &id
	t.SLAFirstResponseDueAt = &fr
	t.SLAResolveDueAt = &rv
	t.UpdatedAt = time.Now().UTC()
}

// MarkFirstResponseBreach is set by the cron evaluator on the first
// pass where the ticket's effective age exceeds the first-response
// budget. Idempotent.
func (t *Ticket) MarkFirstResponseBreach(at time.Time) {
	if t.SLABreachedFirstResponse {
		return
	}
	t.SLABreachedFirstResponse = true
	t.UpdatedAt = at.UTC()
}

// MarkResolveBreach is set by the cron evaluator on the first pass
// where the ticket's effective age exceeds the resolve budget.
// Idempotent.
func (t *Ticket) MarkResolveBreach(at time.Time) {
	if t.SLABreachedResolve {
		return
	}
	t.SLABreachedResolve = true
	t.UpdatedAt = at.UTC()
}

// MarkWarned records that we dispatched the breach_warn_pct warning
// for this ticket. Idempotent — re-runs of the cron loop see a
// non-nil warned_at and skip.
func (t *Ticket) MarkWarned(at time.Time) {
	if t.SLAWarnedAt != nil {
		return
	}
	v := at.UTC()
	t.SLAWarnedAt = &v
	t.UpdatedAt = v
}

// RemainingFirstResponseSeconds returns the seconds left until the
// first-response SLA breach line. Negative means already breached.
// Accounts for pause_seconds via EffectiveAge so a ticket paused for
// 2 hours doesn't burn 2h of SLA budget.
func (t *Ticket) RemainingFirstResponseSeconds(now time.Time) int64 {
	if t.SLAFirstResponseDueAt == nil {
		return 0
	}
	if t.FirstResponseAt != nil {
		// First response already happened — clock stopped.
		return 0
	}
	// SLAFirstResponseDueAt - (CreatedAt + EffectiveAge)
	effectiveNow := t.CreatedAt.Add(t.EffectiveAge(now))
	return int64(t.SLAFirstResponseDueAt.Sub(effectiveNow).Seconds())
}

// RemainingResolveSeconds returns the seconds left until the resolve
// SLA breach line. Negative means already breached.
func (t *Ticket) RemainingResolveSeconds(now time.Time) int64 {
	if t.SLAResolveDueAt == nil {
		return 0
	}
	if t.Status == TicketStatusResolved || t.Status == TicketStatusClosed {
		return 0
	}
	effectiveNow := t.CreatedAt.Add(t.EffectiveAge(now))
	return int64(t.SLAResolveDueAt.Sub(effectiveNow).Seconds())
}

// =====================================================================
// Enum validators
// =====================================================================

func (s TicketStatus) Valid() bool {
	switch s {
	case TicketStatusOpen, TicketStatusAssigned, TicketStatusInProgress,
		TicketStatusPendingCustomer, TicketStatusPendingInternal,
		TicketStatusResolved, TicketStatusClosed:
		return true
	}
	return false
}

func (t TicketType) Valid() bool {
	switch t {
	case TicketTypeTechnical, TicketTypeBilling, TicketTypeComplaint,
		TicketTypeServiceRequest, TicketTypeInformation:
		return true
	}
	return false
}

func (p Priority) Valid() bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}

func (v OpenedVia) Valid() bool {
	switch v {
	case OpenedViaPortal, OpenedViaWhatsApp, OpenedViaPhone, OpenedViaEmail,
		OpenedViaWalkin, OpenedViaAgentInternal, OpenedViaAPI, OpenedViaTechApp:
		return true
	}
	return false
}
