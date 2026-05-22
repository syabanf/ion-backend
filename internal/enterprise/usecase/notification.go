package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
)

// WithNotifications attaches the notification repo. Optional builder —
// when nil, Notify() is a no-op. Keep the producer side resilient to
// "Phase 5 not wired" deployments.
func (s *Service) WithNotifications(repo port.NotificationRepository) *Service {
	s.notifications = repo
	return s
}

// WithNotificationPrefs attaches the per-user mute preferences. When
// wired, every Notify() respects user mutes (default on for unset
// kinds). Without it, every notification fires.
func (s *Service) WithNotificationPrefs(repo port.NotificationPrefRepository) *Service {
	s.notificationPrefs = repo
	return s
}

// Notify is the producer-side entry point used across the codebase to
// fire and forget notifications. Failures are logged + swallowed so a
// notification miss doesn't break the upstream flow (TC-NFR — best
// effort delivery; persistent retry lives in a future polish round).
//
// Honors user preferences via s.notificationPrefs.IsEnabled — a muted
// kind silently drops without DB churn.
func (s *Service) Notify(ctx context.Context, n *domain.Notification) {
	if s.notifications == nil || n == nil {
		return
	}
	if s.notificationPrefs != nil {
		if enabled, _ := s.notificationPrefs.IsEnabled(ctx, n.RecipientUserID, n.Kind); !enabled {
			return
		}
	}
	if err := s.notifications.Create(ctx, n); err != nil {
		if s.log != nil {
			s.log.Warn("notification create failed",
				"kind", n.Kind,
				"recipient", n.RecipientUserID.String(),
				"err", err.Error(),
			)
		}
	}
}

// ListMyNotificationPrefs + UpsertNotificationPref + DeleteNotificationPref
// expose the prefs editor to the FE.

func (s *Service) ListMyNotificationPrefs(ctx context.Context, userID uuid.UUID) ([]port.NotificationPref, error) {
	if s.notificationPrefs == nil {
		return []port.NotificationPref{}, nil
	}
	return s.notificationPrefs.ListForUser(ctx, userID)
}

func (s *Service) UpsertNotificationPref(ctx context.Context, pref port.NotificationPref) error {
	if s.notificationPrefs == nil {
		return nil
	}
	return s.notificationPrefs.Upsert(ctx, pref)
}

func (s *Service) DeleteNotificationPref(ctx context.Context, userID uuid.UUID, kind string) error {
	if s.notificationPrefs == nil {
		return nil
	}
	return s.notificationPrefs.Delete(ctx, userID, kind)
}

// ListMyNotifications returns notifications for the current actor.
// Repo also reports unread count so the FE bell can render the dot.
func (s *Service) ListMyNotifications(ctx context.Context, in port.ListNotificationsInput) ([]domain.Notification, int, int, error) {
	if s.notifications == nil {
		return []domain.Notification{}, 0, 0, nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	return s.notifications.List(ctx, in.RecipientUserID, in.UnreadOnly, limit, in.Offset)
}

func (s *Service) MarkNotificationRead(ctx context.Context, id, recipient uuid.UUID) error {
	if s.notifications == nil {
		return nil
	}
	return s.notifications.MarkRead(ctx, id, recipient)
}

func (s *Service) MarkAllNotificationsRead(ctx context.Context, recipient uuid.UUID) error {
	if s.notifications == nil {
		return nil
	}
	return s.notifications.MarkAllRead(ctx, recipient)
}
