package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

// =====================================================================
// Ticket DTOs
// =====================================================================

type ticketDTO struct {
	ID                string                 `json:"id"`
	TicketNo          string                 `json:"ticket_no"`
	CustomerID        string                 `json:"customer_id"`
	OpenedBy          string                 `json:"opened_by"`
	OpenedVia         string                 `json:"opened_via"`
	TicketType        string                 `json:"ticket_type"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description,omitempty"`
	Status            string                 `json:"status"`
	Priority          string                 `json:"priority"`
	AssignedUserID    *string                `json:"assigned_user_id,omitempty"`
	AssignedTeamID    *string                `json:"assigned_team_id,omitempty"`
	FirstResponseAt   *string                `json:"first_response_at,omitempty"`
	ResolvedAt        *string                `json:"resolved_at,omitempty"`
	ClosedAt          *string                `json:"closed_at,omitempty"`
	EscalatedAt       *string                `json:"escalated_at,omitempty"`
	EscalationLevel   int                    `json:"escalation_level"`
	RelatedWOID       *string                `json:"related_wo_id,omitempty"`
	RelatedInvoiceID  *string                `json:"related_invoice_id,omitempty"`
	PauseSeconds      int64                  `json:"pause_seconds"`
	PausedSince       *string                `json:"paused_since,omitempty"`
	SourceMetadata    map[string]any         `json:"source_metadata,omitempty"`
	CreatedAt         string                 `json:"created_at"`
	UpdatedAt         string                 `json:"updated_at"`
}

func toTicketDTO(t domain.Ticket) ticketDTO {
	return ticketDTO{
		ID:                t.ID.String(),
		TicketNo:          t.TicketNo,
		CustomerID:        t.CustomerID.String(),
		OpenedBy:          t.OpenedBy.String(),
		OpenedVia:         string(t.OpenedVia),
		TicketType:        string(t.TicketType),
		Title:             t.Title,
		Description:       t.Description,
		Status:            string(t.Status),
		Priority:          string(t.Priority),
		AssignedUserID:    uuidPtrString(t.AssignedUserID),
		AssignedTeamID:    uuidPtrString(t.AssignedTeamID),
		FirstResponseAt:   rfc3339Ptr(t.FirstResponseAt),
		ResolvedAt:        rfc3339Ptr(t.ResolvedAt),
		ClosedAt:          rfc3339Ptr(t.ClosedAt),
		EscalatedAt:       rfc3339Ptr(t.EscalatedAt),
		EscalationLevel:   t.EscalationLevel,
		RelatedWOID:       uuidPtrString(t.RelatedWOID),
		RelatedInvoiceID:  uuidPtrString(t.RelatedInvoiceID),
		PauseSeconds:      t.PauseSeconds,
		PausedSince:       rfc3339Ptr(t.PausedSince),
		SourceMetadata:    t.SourceMetadata,
		CreatedAt:         t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         t.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Ticket event DTO
// =====================================================================

type ticketEventDTO struct {
	ID        string         `json:"id"`
	TicketID  string         `json:"ticket_id"`
	Kind      string         `json:"kind"`
	Payload   map[string]any `json:"payload,omitempty"`
	ActorID   *string        `json:"actor_id,omitempty"`
	ActorRole string         `json:"actor_role,omitempty"`
	CreatedAt string         `json:"created_at"`
}

func toTicketEventDTO(e domain.TicketEvent) ticketEventDTO {
	return ticketEventDTO{
		ID:        e.ID.String(),
		TicketID:  e.TicketID.String(),
		Kind:      string(e.Kind),
		Payload:   e.Payload,
		ActorID:   uuidPtrString(e.ActorID),
		ActorRole: e.ActorRole,
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Comment DTO
// =====================================================================

type commentDTO struct {
	ID          string                     `json:"id"`
	TicketID    string                     `json:"ticket_id"`
	AuthorID    string                     `json:"author_id"`
	AuthorRole  string                     `json:"author_role,omitempty"`
	Body        string                     `json:"body"`
	IsInternal  bool                       `json:"is_internal"`
	Attachments []domain.CommentAttachment `json:"attachments"`
	CreatedAt   string                     `json:"created_at"`
	UpdatedAt   string                     `json:"updated_at"`
	EditedAt    *string                    `json:"edited_at,omitempty"`
	DeletedAt   *string                    `json:"deleted_at,omitempty"`
}

func toCommentDTO(c domain.Comment) commentDTO {
	att := c.Attachments
	if att == nil {
		att = []domain.CommentAttachment{}
	}
	return commentDTO{
		ID:          c.ID.String(),
		TicketID:    c.TicketID.String(),
		AuthorID:    c.AuthorID.String(),
		AuthorRole:  c.AuthorRole,
		Body:        c.Body,
		IsInternal:  c.IsInternal,
		Attachments: att,
		CreatedAt:   c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.UTC().Format(time.RFC3339),
		EditedAt:    rfc3339Ptr(c.EditedAt),
		DeletedAt:   rfc3339Ptr(c.DeletedAt),
	}
}

// =====================================================================
// Mention DTO
// =====================================================================

type mentionDTO struct {
	ID                string  `json:"id"`
	TicketID          string  `json:"ticket_id"`
	CommentID         *string `json:"comment_id,omitempty"`
	MentionedUserID   string  `json:"mentioned_user_id"`
	MentionedByUserID string  `json:"mentioned_by_user_id"`
	ReadAt            *string `json:"read_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

func toMentionDTO(m domain.Mention) mentionDTO {
	return mentionDTO{
		ID:                m.ID.String(),
		TicketID:          m.TicketID.String(),
		CommentID:         uuidPtrString(m.CommentID),
		MentionedUserID:   m.MentionedUserID.String(),
		MentionedByUserID: m.MentionedByUserID.String(),
		ReadAt:            rfc3339Ptr(m.ReadAt),
		CreatedAt:         m.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toMentionDTOs(ms []domain.Mention) []mentionDTO {
	out := make([]mentionDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMentionDTO(m))
	}
	return out
}

// =====================================================================
// Channel DTO
// =====================================================================

type channelDTO struct {
	ID            string         `json:"id"`
	Code          string         `json:"code"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	IsActive      bool           `json:"is_active"`
	ConfigPayload map[string]any `json:"config_payload,omitempty"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
}

func toChannelDTO(c domain.Channel) channelDTO {
	return channelDTO{
		ID:            c.ID.String(),
		Code:          c.Code,
		Name:          c.Name,
		Kind:          string(c.Kind),
		IsActive:      c.IsActive,
		ConfigPayload: c.ConfigPayload,
		CreatedAt:     c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Helpers
// =====================================================================

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func uuidPtrString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}
