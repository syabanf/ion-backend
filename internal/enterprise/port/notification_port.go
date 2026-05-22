package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Inputs
// =====================================================================

type ListNotificationsInput struct {
	RecipientUserID uuid.UUID
	UnreadOnly      bool
	Limit           int
	Offset          int
}

// =====================================================================
// UseCase
// =====================================================================

type NotificationUseCase interface {
	// Producer-side: any service module can fire-and-forget via this
	// method. Failures are logged but DO NOT block the calling flow —
	// the dispatch is best-effort.
	Notify(ctx context.Context, n *domain.Notification)

	// Consumer-side (inbox).
	ListMyNotifications(ctx context.Context, in ListNotificationsInput) ([]domain.Notification, int, int, error)
	MarkNotificationRead(ctx context.Context, id, recipient uuid.UUID) error
	MarkAllNotificationsRead(ctx context.Context, recipient uuid.UUID) error
}

// =====================================================================
// Repository
// =====================================================================

type NotificationRepository interface {
	Create(ctx context.Context, n *domain.Notification) error
	List(ctx context.Context, recipientUserID uuid.UUID, unreadOnly bool, limit, offset int) ([]domain.Notification, int, int, error) // items, total, unread
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Notification, error)
	MarkRead(ctx context.Context, id, recipient uuid.UUID) error
	MarkAllRead(ctx context.Context, recipient uuid.UUID) error
}

// =====================================================================
// Notification preferences (pre-launch polish)
// =====================================================================

type NotificationPref struct {
	UserID  uuid.UUID
	Kind    string // exact kind, "module.*" wildcard, or "*"
	Enabled bool
}

type NotificationPrefRepository interface {
	ListForUser(ctx context.Context, userID uuid.UUID) ([]NotificationPref, error)
	Upsert(ctx context.Context, pref NotificationPref) error
	Delete(ctx context.Context, userID uuid.UUID, kind string) error
	// IsEnabled is the hot-path check called from s.Notify before every
	// insert. Returns true if the user has NOT explicitly muted the kind
	// (default-on). Wildcards: "*" mutes all; "boq.*" mutes any kind
	// whose prefix matches.
	IsEnabled(ctx context.Context, userID uuid.UUID, kind string) (bool, error)
}
