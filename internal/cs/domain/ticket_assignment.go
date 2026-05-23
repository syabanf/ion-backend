package domain

import (
	"time"

	"github.com/google/uuid"
)

// =====================================================================
// AssignmentEvent — immutable audit row for cs.ticket_assignments_history.
//
// Unlike TicketEvent (which is a generic timeline kind), this row
// carries strongly-typed from/to columns so we can answer questions
// like "who has been on this ticket?" without parsing payload JSON.
// =====================================================================

// AssignmentKind matches cs.ticket_assignments_history.assignment_kind.
type AssignmentKind string

const (
	AssignmentKindUser     AssignmentKind = "user"
	AssignmentKindTeam     AssignmentKind = "team"
	AssignmentKindTransfer AssignmentKind = "transfer"
	AssignmentKindUnassign AssignmentKind = "unassign"
)

func (k AssignmentKind) Valid() bool {
	switch k {
	case AssignmentKindUser, AssignmentKindTeam, AssignmentKindTransfer, AssignmentKindUnassign:
		return true
	}
	return false
}

// AssignmentEvent is the audit row. No state machine — it's an
// append-only entry written by the usecase after every assignment.
type AssignmentEvent struct {
	ID             uuid.UUID
	TicketID       uuid.UUID
	AssignmentKind AssignmentKind
	FromUserID     *uuid.UUID
	ToUserID       *uuid.UUID
	FromTeamID     *uuid.UUID
	ToTeamID       *uuid.UUID
	Reason         string
	AssignedBy     *uuid.UUID
	AssignedAt     time.Time
}

// NewAssignmentEvent constructs a fresh audit row.
func NewAssignmentEvent(
	ticketID uuid.UUID,
	kind AssignmentKind,
	fromUser, toUser, fromTeam, toTeam *uuid.UUID,
	by *uuid.UUID,
	reason string,
) *AssignmentEvent {
	return &AssignmentEvent{
		ID:             uuid.New(),
		TicketID:       ticketID,
		AssignmentKind: kind,
		FromUserID:     fromUser,
		ToUserID:       toUser,
		FromTeamID:     fromTeam,
		ToTeamID:       toTeam,
		Reason:         reason,
		AssignedBy:     by,
		AssignedAt:     time.Now().UTC(),
	}
}
