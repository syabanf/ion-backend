package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Mention is one @user ping. State: created → read (via MarkRead).
type Mention struct {
	ID                 uuid.UUID
	TicketID           uuid.UUID
	CommentID          *uuid.UUID
	MentionedUserID    uuid.UUID
	MentionedByUserID  uuid.UUID
	ReadAt             *time.Time
	CreatedAt          time.Time
}

// NewMention constructs a fresh mention in the unread state.
func NewMention(ticketID, commentID, mentionedUserID, mentionedByUserID uuid.UUID) (*Mention, error) {
	if ticketID == uuid.Nil {
		return nil, errors.Validation("cs.mention.ticket_required", "ticket_id is required")
	}
	if mentionedUserID == uuid.Nil {
		return nil, errors.Validation("cs.mention.user_required", "mentioned_user_id is required")
	}
	if mentionedByUserID == uuid.Nil {
		return nil, errors.Validation("cs.mention.actor_required", "mentioned_by_user_id is required")
	}
	m := &Mention{
		ID:                uuid.New(),
		TicketID:          ticketID,
		MentionedUserID:   mentionedUserID,
		MentionedByUserID: mentionedByUserID,
		CreatedAt:         time.Now().UTC(),
	}
	if commentID != uuid.Nil {
		c := commentID
		m.CommentID = &c
	}
	return m, nil
}

// MarkRead stamps read_at. Idempotent on already-read.
func (m *Mention) MarkRead(at time.Time) {
	if m.ReadAt != nil {
		return
	}
	v := at.UTC()
	m.ReadAt = &v
}

// IsUnread is a convenience for the dashboard query.
func (m *Mention) IsUnread() bool { return m.ReadAt == nil }
