package domain

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Comment is a thread message on a ticket. Comments can be internal
// (agent-only) or customer-visible.
//
// Mentions are extracted from Body by ExtractMentions at create time;
// the usecase resolves the usernames to user IDs via the
// port.MentionResolver and writes the cs.ticket_mentions rows.
type Comment struct {
	ID          uuid.UUID
	TicketID    uuid.UUID
	AuthorID    uuid.UUID
	AuthorRole  string
	Body        string
	IsInternal  bool
	Attachments []CommentAttachment
	CreatedAt   time.Time
	UpdatedAt   time.Time
	EditedAt    *time.Time
	DeletedAt   *time.Time
}

// CommentAttachment is an inline reference to an upload — the actual
// blob lives in cs.ticket_attachments. Kept inline on the comment for
// convenience and so the HTTP DTO can return them in one shot.
type CommentAttachment struct {
	URL      string `json:"url"`
	Name     string `json:"name"`
	SizeByte int64  `json:"size_bytes,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// NewComment validates body + author and stamps timestamps.
func NewComment(ticketID, authorID uuid.UUID, authorRole, body string, isInternal bool, attachments []CommentAttachment) (*Comment, error) {
	if ticketID == uuid.Nil {
		return nil, errors.Validation("cs.comment.ticket_required", "ticket_id is required")
	}
	if authorID == uuid.Nil {
		return nil, errors.Validation("cs.comment.author_required", "author_id is required")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.Validation("cs.comment.body_required", "comment body is required")
	}
	now := time.Now().UTC()
	return &Comment{
		ID:          uuid.New(),
		TicketID:    ticketID,
		AuthorID:    authorID,
		AuthorRole:  authorRole,
		Body:        body,
		IsInternal:  isInternal,
		Attachments: attachments,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Edit updates the body + stamps edited_at. Soft-delete is via
// Delete().
func (c *Comment) Edit(newBody string) error {
	newBody = strings.TrimSpace(newBody)
	if newBody == "" {
		return errors.Validation("cs.comment.body_required", "comment body is required")
	}
	if c.DeletedAt != nil {
		return errors.Conflict("cs.comment.deleted", "deleted comments cannot be edited")
	}
	now := time.Now().UTC()
	c.Body = newBody
	c.EditedAt = &now
	c.UpdatedAt = now
	return nil
}

// Delete soft-deletes (stamps deleted_at, leaves the row in place so
// audit history is preserved).
func (c *Comment) Delete() {
	if c.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	c.DeletedAt = &now
	c.UpdatedAt = now
}

// mentionPattern matches @ followed by 1+ word-chars / dots / dashes.
// Conservative — won't match email addresses (the @ must be preceded
// by whitespace or start-of-string) and won't capture trailing punct.
var mentionPattern = regexp.MustCompile(`(?:^|[\s(\[{])@([a-zA-Z][a-zA-Z0-9_.\-]{1,50})`)

// ExtractMentions parses @username tokens out of body and returns a
// de-duplicated list. The usernames are returned in lowercase so the
// resolver can do case-insensitive lookups.
//
// Pass a body to scan; the result is the set of literal usernames
// (without the @). The resolver in port.MentionResolver maps them to
// user IDs — invalid mentions get silently dropped at that stage so
// `@maybeAUser` text in a comment doesn't break the save.
func ExtractMentions(body string) []string {
	if body == "" {
		return nil
	}
	matches := mentionPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		name := strings.ToLower(m[1])
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
