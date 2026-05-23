package domain

import (
	"time"

	"github.com/google/uuid"
)

// EventKind mirrors cs.ticket_events.kind. Append-only.
type EventKind string

const (
	EventKindStatusChange   EventKind = "status_change"
	EventKindPriorityChange EventKind = "priority_change"
	EventKindAssignment     EventKind = "assignment"
	EventKindReassignment   EventKind = "reassignment"
	EventKindComment        EventKind = "comment"
	EventKindAttachment     EventKind = "attachment"
	EventKindMention        EventKind = "mention"
	EventKindClose          EventKind = "close"
	EventKindReopen         EventKind = "reopen"
	EventKindEscalation     EventKind = "escalation"
	EventKindSLABreach      EventKind = "sla_breach"
	EventKindChannelChange  EventKind = "channel_change"
)

// TicketEvent is an append-only timeline entry.
type TicketEvent struct {
	ID        uuid.UUID
	TicketID  uuid.UUID
	Kind      EventKind
	Payload   map[string]any
	ActorID   *uuid.UUID
	ActorRole string
	CreatedAt time.Time
}

// NewTicketEvent constructs a timeline entry. Used by the usecase
// layer after every successful state transition. Payload is the only
// per-kind variable.
func NewTicketEvent(ticketID uuid.UUID, kind EventKind, actorID *uuid.UUID, actorRole string, payload map[string]any) *TicketEvent {
	return &TicketEvent{
		ID:        uuid.New(),
		TicketID:  ticketID,
		Kind:      kind,
		Payload:   payload,
		ActorID:   actorID,
		ActorRole: actorRole,
		CreatedAt: time.Now().UTC(),
	}
}
