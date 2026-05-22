package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type NotificationRepository struct {
	pool *pgxpool.Pool
}

func NewNotificationRepository(pool *pgxpool.Pool) *NotificationRepository {
	return &NotificationRepository{pool: pool}
}

var _ port.NotificationRepository = (*NotificationRepository)(nil)

const notificationCols = `
	id, recipient_user_id, kind, subject_type, subject_id,
	title, COALESCE(body, ''), severity, read_at, created_at
`

func (r *NotificationRepository) Create(ctx context.Context, n *domain.Notification) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.notifications
			(id, recipient_user_id, kind, subject_type, subject_id,
			 title, body, severity, read_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		n.ID, n.RecipientUserID, n.Kind, n.SubjectType, n.SubjectID,
		n.Title, n.Body, string(n.Severity), n.ReadAt, n.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "notification", "insert notification")
	}
	return nil
}

func (r *NotificationRepository) List(
	ctx context.Context, recipient uuid.UUID, unreadOnly bool, limit, offset int,
) ([]domain.Notification, int, int, error) {
	if limit <= 0 {
		limit = 50
	}
	var wh []string
	args := []any{recipient}
	wh = append(wh, "recipient_user_id = $1")
	if unreadOnly {
		wh = append(wh, "read_at IS NULL")
	}
	where := " WHERE " + strings.Join(wh, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.notifications`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, 0, derrors.Wrap(derrors.KindInternal, "db.notification_count", "count", err)
	}
	var unread int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.notifications WHERE recipient_user_id = $1 AND read_at IS NULL`,
		recipient,
	).Scan(&unread); err != nil {
		return nil, 0, 0, derrors.Wrap(derrors.KindInternal, "db.notification_unread_count", "count unread", err)
	}

	args = append(args, limit, offset)
	sql := `SELECT ` + notificationCols + ` FROM enterprise.notifications` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, 0, derrors.Wrap(derrors.KindInternal, "db.notification_list", "list notifications", err)
	}
	defer rows.Close()
	out := []domain.Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		out = append(out, n)
	}
	return out, total, unread, nil
}

func (r *NotificationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Notification, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+notificationCols+` FROM enterprise.notifications WHERE id = $1`, id)
	n, err := scanNotification(row)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *NotificationRepository) MarkRead(ctx context.Context, id, recipient uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE enterprise.notifications SET read_at = NOW() WHERE id = $1 AND recipient_user_id = $2 AND read_at IS NULL`,
		id, recipient,
	)
	if err != nil {
		return mapDBError(err, "notification", "mark read")
	}
	if tag.RowsAffected() == 0 {
		// Either already read or doesn't belong to this recipient — both
		// are harmless from the caller's perspective.
		return nil
	}
	return nil
}

func (r *NotificationRepository) MarkAllRead(ctx context.Context, recipient uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE enterprise.notifications SET read_at = NOW() WHERE recipient_user_id = $1 AND read_at IS NULL`,
		recipient,
	)
	if err != nil {
		return mapDBError(err, "notification", "mark all read")
	}
	return nil
}

func scanNotification(row pgx.Row) (domain.Notification, error) {
	var (
		n        domain.Notification
		severity string
	)
	err := row.Scan(
		&n.ID, &n.RecipientUserID, &n.Kind, &n.SubjectType, &n.SubjectID,
		&n.Title, &n.Body, &severity, &n.ReadAt, &n.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, derrors.NotFound("notification.not_found", "notification not found")
	}
	if err != nil {
		return domain.Notification{}, derrors.Wrap(derrors.KindInternal, "db.notification_scan", "scan", err)
	}
	n.Severity = domain.NotificationSeverity(severity)
	return n, nil
}
