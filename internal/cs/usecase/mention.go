package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
)

// MentionService implements port.MentionUseCase.
//
// MarkAsRead enforces that only the mentioned user can mark their own
// mention read — supervisors can't preemptively clear someone else's
// inbox.
type MentionService struct {
	mentions port.TicketMentionRepository
	notifier port.NotificationBridge
}

func NewMentionService(mentions port.TicketMentionRepository, notifier port.NotificationBridge) *MentionService {
	return &MentionService{mentions: mentions, notifier: notifier}
}

var _ port.MentionUseCase = (*MentionService)(nil)

func (s *MentionService) ListMyMentions(ctx context.Context, f port.MentionListFilter) ([]domain.Mention, error) {
	return s.mentions.List(ctx, f)
}

func (s *MentionService) MarkAsRead(ctx context.Context, mentionID, byUserID uuid.UUID) error {
	m, err := s.mentions.FindByID(ctx, mentionID)
	if err != nil {
		return err
	}
	if m.MentionedUserID != byUserID {
		return errors.Forbidden("cs.mention.not_addressee",
			"only the mentioned user can mark this mention read")
	}
	return s.mentions.MarkRead(ctx, mentionID, time.Now().UTC())
}

// ReDispatchUnreadOlderThan is the mention-reminder cron entrypoint.
// Returns the number of re-pings dispatched.
func (s *MentionService) ReDispatchUnreadOlderThan(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if s.notifier == nil {
		return 0, nil
	}
	rows, err := s.mentions.ListUnreadOlderThan(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	for i := range rows {
		s.notifier.NotifyMention(ctx, &rows[i], "", "")
	}
	return len(rows), nil
}
