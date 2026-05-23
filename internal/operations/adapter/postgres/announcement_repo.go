package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// AnnouncementRepository persists operations.internal_announcements.
type AnnouncementRepository struct {
	pool *pgxpool.Pool
}

func NewAnnouncementRepository(pool *pgxpool.Pool) *AnnouncementRepository {
	return &AnnouncementRepository{pool: pool}
}

var _ port.AnnouncementRepository = (*AnnouncementRepository)(nil)

func (r *AnnouncementRepository) Create(ctx context.Context, a *domain.Announcement) error {
	if a == nil {
		return derrors.Validation("announcement.nil", "announcement is nil")
	}
	channelsJSON, _ := json.Marshal(a.Channels)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO operations.internal_announcements
			(id, title, body, severity, targeting, channels,
			 scheduled_at, expires_at, dispatch_status, target_audience,
			 sent_at, sent_count, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, '{}'::jsonb, $5::jsonb,
		        $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		a.ID, a.Title, a.Body, string(a.Severity),
		string(channelsJSON),
		a.ScheduledAt, a.ExpiresAt, string(a.DispatchStatus), string(a.TargetAudience),
		a.SentAt, a.SentCount, a.CreatedBy, a.CreatedAt, a.UpdatedAt,
	)
	return mapDBError(err, "announcement", "insert announcement")
}

func (r *AnnouncementRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Announcement, error) {
	a, err := r.scanRow(ctx, `WHERE id = $1`, id)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (r *AnnouncementRepository) ListPending(ctx context.Context, asOf time.Time, limit int) ([]domain.Announcement, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, title, body, severity, COALESCE(channels::text, '[]'),
		       scheduled_at, expires_at, sent_at, dispatched_at,
		       COALESCE(dispatch_status, 'pending'),
		       COALESCE(target_audience, 'all'),
		       COALESCE(sent_count, 0),
		       created_by, created_at, updated_at
		  FROM operations.internal_announcements
		 WHERE dispatch_status = 'pending'
		   AND (scheduled_at IS NULL OR scheduled_at <= $1)
		 ORDER BY created_at ASC
		 LIMIT $2
	`, asOf, limit)
	if err != nil {
		return nil, mapDBError(err, "announcement", "list pending")
	}
	defer rows.Close()
	out := []domain.Announcement{}
	for rows.Next() {
		a, err := scanAnnouncementRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *AnnouncementRepository) Update(ctx context.Context, a *domain.Announcement) error {
	if a == nil {
		return derrors.Validation("announcement.nil", "announcement is nil")
	}
	channelsJSON, _ := json.Marshal(a.Channels)
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.internal_announcements
		   SET title          = $2,
		       body           = $3,
		       severity       = $4,
		       channels       = $5::jsonb,
		       scheduled_at   = $6,
		       expires_at     = $7,
		       sent_at        = $8,
		       dispatched_at  = $9,
		       dispatch_status= $10,
		       target_audience= $11,
		       sent_count     = $12,
		       updated_at     = NOW()
		 WHERE id = $1
	`,
		a.ID, a.Title, a.Body, string(a.Severity), string(channelsJSON),
		a.ScheduledAt, a.ExpiresAt, a.SentAt, a.DispatchedAt,
		string(a.DispatchStatus), string(a.TargetAudience), a.SentCount,
	)
	if err != nil {
		return mapDBError(err, "announcement", "update announcement")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("announcement.not_found", "announcement not found")
	}
	return nil
}

func (r *AnnouncementRepository) scanRow(ctx context.Context, where string, args ...any) (*domain.Announcement, error) {
	q := `
		SELECT id, title, body, severity, COALESCE(channels::text, '[]'),
		       scheduled_at, expires_at, sent_at, dispatched_at,
		       COALESCE(dispatch_status, 'pending'),
		       COALESCE(target_audience, 'all'),
		       COALESCE(sent_count, 0),
		       created_by, created_at, updated_at
		  FROM operations.internal_announcements ` + where
	row := r.pool.QueryRow(ctx, q, args...)
	return scanAnnouncementRow(row)
}

// rowScanner is the minimal interface pgx.Row and pgx.Rows both satisfy.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAnnouncementRow(row rowScanner) (*domain.Announcement, error) {
	var a domain.Announcement
	var severity, status, audience, channelsJSON string
	err := row.Scan(
		&a.ID, &a.Title, &a.Body, &severity, &channelsJSON,
		&a.ScheduledAt, &a.ExpiresAt, &a.SentAt, &a.DispatchedAt,
		&status, &audience, &a.SentCount,
		&a.CreatedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.Severity = domain.NormalizeSeverity(severity)
	a.DispatchStatus = domain.AnnouncementDispatchStatus(status)
	a.TargetAudience = domain.NormalizeAudience(audience)
	if channelsJSON != "" {
		var ch []string
		if err := json.Unmarshal([]byte(channelsJSON), &ch); err == nil {
			a.Channels = ch
		}
	}
	return &a, nil
}

// =====================================================================
// AnnouncementRecipientRepository
// =====================================================================

type AnnouncementRecipientRepository struct {
	pool *pgxpool.Pool
}

func NewAnnouncementRecipientRepository(pool *pgxpool.Pool) *AnnouncementRecipientRepository {
	return &AnnouncementRecipientRepository{pool: pool}
}

var _ port.AnnouncementRecipientRepository = (*AnnouncementRecipientRepository)(nil)

func (r *AnnouncementRecipientRepository) CreateBatch(ctx context.Context, rows []domain.AnnouncementRecipient) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	written := 0
	for _, row := range rows {
		tag, err := r.pool.Exec(ctx, `
			INSERT INTO operations.announcement_recipients
				(id, announcement_id, user_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (announcement_id, user_id) DO NOTHING
		`, row.ID, row.AnnouncementID, row.UserID)
		if err != nil {
			return written, mapDBError(err, "announcement_recipient", "insert recipient")
		}
		written += int(tag.RowsAffected())
	}
	return written, nil
}

func (r *AnnouncementRecipientRepository) ListByAnnouncement(ctx context.Context, announcementID uuid.UUID) ([]domain.AnnouncementRecipient, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, announcement_id, user_id,
		       delivered_at, read_at,
		       COALESCE(channel, ''), COALESCE(error_msg, '')
		  FROM operations.announcement_recipients
		 WHERE announcement_id = $1
		 ORDER BY created_at ASC
	`, announcementID)
	if err != nil {
		return nil, mapDBError(err, "announcement_recipient", "list by announcement")
	}
	defer rows.Close()
	out := []domain.AnnouncementRecipient{}
	for rows.Next() {
		var rec domain.AnnouncementRecipient
		if err := rows.Scan(&rec.ID, &rec.AnnouncementID, &rec.UserID,
			&rec.DeliveredAt, &rec.ReadAt, &rec.Channel, &rec.ErrorMsg); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *AnnouncementRecipientRepository) ListMyInbox(ctx context.Context, userID uuid.UUID, unreadOnly bool, limit int) ([]port.AnnouncementInboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	whereExtra := ""
	if unreadOnly {
		whereExtra = " AND rec.read_at IS NULL "
	}
	q := `
		SELECT rec.id, rec.announcement_id, a.title, a.body, a.severity,
		       rec.delivered_at, rec.read_at, rec.created_at
		  FROM operations.announcement_recipients rec
		  JOIN operations.internal_announcements a ON a.id = rec.announcement_id
		 WHERE rec.user_id = $1 ` + whereExtra + `
		 ORDER BY rec.created_at DESC
		 LIMIT $2`
	rows, err := r.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, mapDBError(err, "announcement_recipient", "list inbox")
	}
	defer rows.Close()
	out := []port.AnnouncementInboxEntry{}
	for rows.Next() {
		var e port.AnnouncementInboxEntry
		var severity string
		if err := rows.Scan(&e.RecipientID, &e.AnnouncementID, &e.Title, &e.Body, &severity,
			&e.DeliveredAt, &e.ReadAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Severity = domain.NormalizeSeverity(severity)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *AnnouncementRecipientRepository) MarkDelivered(ctx context.Context, id uuid.UUID, channel string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.announcement_recipients
		   SET delivered_at = $2,
		       channel = $3,
		       error_msg = NULL
		 WHERE id = $1
	`, id, at, channel)
	if err != nil {
		return mapDBError(err, "announcement_recipient", "mark delivered")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("announcement_recipient.not_found", "recipient row not found")
	}
	return nil
}

func (r *AnnouncementRecipientRepository) MarkRead(ctx context.Context, announcementID, userID uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.announcement_recipients
		   SET read_at = COALESCE(read_at, $3)
		 WHERE announcement_id = $1
		   AND user_id = $2
	`, announcementID, userID, at)
	if err != nil {
		return mapDBError(err, "announcement_recipient", "mark read")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("announcement_recipient.not_found", "recipient row not found")
	}
	return nil
}

func (r *AnnouncementRecipientRepository) CountDelivered(ctx context.Context, announcementID uuid.UUID) (int, int, error) {
	var delivered, total int
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE delivered_at IS NOT NULL),
			COUNT(*)
		  FROM operations.announcement_recipients
		 WHERE announcement_id = $1
	`, announcementID).Scan(&delivered, &total)
	if err != nil {
		return 0, 0, mapDBError(err, "announcement_recipient", "count delivered")
	}
	return delivered, total, nil
}
