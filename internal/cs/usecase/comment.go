package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
)

// CommentService implements port.CommentUseCase.
//
// AddComment parses @mentions out of the body, resolves the usernames
// via the MentionResolver, persists the comment + mentions, and fires
// notification bridges. Unknown usernames are silently dropped — a
// typo in `@maybeAUser` must not fail the save.
type CommentService struct {
	tickets  port.TicketRepository
	comments port.TicketCommentRepository
	mentions port.TicketMentionRepository
	events   port.TicketEventRepository
	resolver port.MentionResolver
	notifier port.NotificationBridge
}

func NewCommentService(
	tickets port.TicketRepository,
	comments port.TicketCommentRepository,
	mentions port.TicketMentionRepository,
	events port.TicketEventRepository,
	resolver port.MentionResolver,
	notifier port.NotificationBridge,
) *CommentService {
	return &CommentService{
		tickets:  tickets,
		comments: comments,
		mentions: mentions,
		events:   events,
		resolver: resolver,
		notifier: notifier,
	}
}

var _ port.CommentUseCase = (*CommentService)(nil)

// AddComment persists a comment + extracts/resolves @mentions.
// Returns the comment and the list of persisted mentions (so the
// HTTP layer can echo them in the response if it wants to).
func (s *CommentService) AddComment(ctx context.Context, in port.CreateCommentInput) (*domain.Comment, []domain.Mention, error) {
	c, err := domain.NewComment(in.TicketID, in.AuthorID, in.AuthorRole, in.Body, in.IsInternal, in.Attachments)
	if err != nil {
		return nil, nil, err
	}

	// Verify the ticket exists + is not closed (closed tickets allow
	// supervisor edits via a separate path; for now we refuse new
	// comments on closed tickets to keep the state-machine clean).
	t, err := s.tickets.FindByID(ctx, in.TicketID)
	if err != nil {
		return nil, nil, err
	}
	if t.IsTerminal() {
		return nil, nil, errors.Conflict("cs.comment.ticket_closed", "cannot comment on a closed ticket; reopen first")
	}

	if err := s.comments.Insert(ctx, c); err != nil {
		return nil, nil, err
	}

	// Comment event for the timeline.
	if s.events != nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(in.TicketID, domain.EventKindComment, &in.AuthorID, in.AuthorRole, map[string]any{
			"comment_id":  c.ID.String(),
			"is_internal": in.IsInternal,
		}))
	}

	// Mark first_response_at if this is the first agent touch on a
	// still-open ticket. Internal notes don't count as a first
	// response — they're agent-to-agent.
	if !in.IsInternal && t.FirstResponseAt == nil {
		t.MarkFirstResponse(time.Now().UTC())
		_ = s.tickets.Update(ctx, t)
	}

	mentions, err := s.resolveAndPersistMentions(ctx, t, c)
	if err != nil {
		// Don't fail the comment save on a mention-resolution glitch;
		// the comment is already in. Log via the event repo so the
		// audit trail captures the failure.
		if s.events != nil {
			_ = s.events.Insert(ctx, domain.NewTicketEvent(in.TicketID, domain.EventKindMention, &in.AuthorID, in.AuthorRole, map[string]any{
				"error":      "mention_resolution_failed",
				"detail":     err.Error(),
				"comment_id": c.ID.String(),
			}))
		}
		return c, nil, nil
	}

	return c, mentions, nil
}

func (s *CommentService) resolveAndPersistMentions(ctx context.Context, t *domain.Ticket, c *domain.Comment) ([]domain.Mention, error) {
	names := domain.ExtractMentions(c.Body)
	if len(names) == 0 || s.resolver == nil {
		return nil, nil
	}
	resolved, err := s.resolver.Resolve(ctx, names)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	out := make([]domain.Mention, 0, len(resolved))
	for _, uid := range resolved {
		if uid == c.AuthorID {
			// Don't ping yourself.
			continue
		}
		m, err := domain.NewMention(t.ID, c.ID, uid, c.AuthorID)
		if err != nil {
			continue
		}
		if err := s.mentions.Insert(ctx, m); err != nil {
			// Duplicate (re-parse of same body) is fine — just skip.
			continue
		}
		if s.events != nil {
			_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindMention, &c.AuthorID, c.AuthorRole, map[string]any{
				"mention_id":     m.ID.String(),
				"mentioned_user": uid.String(),
				"comment_id":     c.ID.String(),
			}))
		}
		if s.notifier != nil {
			s.notifier.NotifyMention(ctx, m, t.Title, "")
		}
		out = append(out, *m)
	}
	return out, nil
}

// EditComment lets the author update body (and bumps edited_at). A
// supervisor can edit any comment; non-supervisors can only edit
// their own.
func (s *CommentService) EditComment(ctx context.Context, commentID uuid.UUID, newBody string, byUserID uuid.UUID, isSupervisor bool) (*domain.Comment, error) {
	c, err := s.comments.FindByID(ctx, commentID)
	if err != nil {
		return nil, err
	}
	if !isSupervisor && c.AuthorID != byUserID {
		return nil, errors.Forbidden("cs.comment.not_author", "only the author or a supervisor can edit this comment")
	}
	if err := c.Edit(newBody); err != nil {
		return nil, err
	}
	if err := s.comments.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// DeleteComment soft-deletes via deleted_at. Same author/supervisor
// gate as EditComment.
func (s *CommentService) DeleteComment(ctx context.Context, commentID, byUserID uuid.UUID, isSupervisor bool) error {
	c, err := s.comments.FindByID(ctx, commentID)
	if err != nil {
		return err
	}
	if !isSupervisor && c.AuthorID != byUserID {
		return errors.Forbidden("cs.comment.not_author", "only the author or a supervisor can delete this comment")
	}
	c.Delete()
	return s.comments.Update(ctx, c)
}

func (s *CommentService) ListComments(ctx context.Context, f port.CommentListFilter) ([]domain.Comment, error) {
	return s.comments.List(ctx, f)
}
