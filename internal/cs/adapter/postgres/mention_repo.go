package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// TicketMentionRepository implements port.TicketMentionRepository
// against cs.ticket_mentions.
type TicketMentionRepository struct {
	pool *pgxpool.Pool
}

func NewTicketMentionRepository(pool *pgxpool.Pool) *TicketMentionRepository {
	return &TicketMentionRepository{pool: pool}
}

var _ port.TicketMentionRepository = (*TicketMentionRepository)(nil)

const mentionCols = `
	id, ticket_id, comment_id, mentioned_user_id, mentioned_by_user_id, read_at, created_at
`

func (r *TicketMentionRepository) Insert(ctx context.Context, m *domain.Mention) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs.ticket_mentions
			(id, ticket_id, comment_id, mentioned_user_id, mentioned_by_user_id, read_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		m.ID, m.TicketID, m.CommentID, m.MentionedUserID, m.MentionedByUserID,
		m.ReadAt, m.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.mention", "insert mention")
	}
	return nil
}

func (r *TicketMentionRepository) List(ctx context.Context, f port.MentionListFilter) ([]domain.Mention, error) {
	var wh []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		wh = append(wh, fmt.Sprintf(cond, len(args)))
	}
	add("mentioned_user_id = $%d", f.MentionedUserID)
	if f.UnreadOnly {
		wh = append(wh, "read_at IS NULL")
	}
	if f.Since != nil {
		add("created_at >= $%d", *f.Since)
	}
	where := " WHERE " + strings.Join(wh, " AND ")
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + mentionCols + ` FROM cs.ticket_mentions` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.mention.list", "list mentions", err)
	}
	defer rows.Close()
	out := []domain.Mention{}
	for rows.Next() {
		m, err := scanMention(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (r *TicketMentionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Mention, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+mentionCols+` FROM cs.ticket_mentions WHERE id = $1`, id)
	m, err := scanMention(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *TicketMentionRepository) MarkRead(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.ticket_mentions
		   SET read_at = $2
		 WHERE id = $1
		   AND read_at IS NULL
	`, id, at)
	if err != nil {
		return mapDBError(err, "cs.mention", "mark mention read")
	}
	if tag.RowsAffected() == 0 {
		// Either already-read or doesn't exist. Idempotent on
		// already-read; only flag "not found" if FindByID misses too.
		if _, err := r.FindByID(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (r *TicketMentionRepository) ListUnreadOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.Mention, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+mentionCols+`
		  FROM cs.ticket_mentions
		 WHERE read_at IS NULL
		   AND created_at < $1
		 ORDER BY created_at ASC
		 LIMIT $2
	`, cutoff, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.mention.list_unread", "list unread mentions", err)
	}
	defer rows.Close()
	out := []domain.Mention{}
	for rows.Next() {
		m, err := scanMention(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func scanMention(row pgx.Row) (domain.Mention, error) {
	var m domain.Mention
	err := row.Scan(
		&m.ID, &m.TicketID, &m.CommentID, &m.MentionedUserID, &m.MentionedByUserID,
		&m.ReadAt, &m.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Mention{}, derrors.NotFound("cs.mention.not_found", "mention not found")
	}
	if err != nil {
		return domain.Mention{}, derrors.Wrap(derrors.KindInternal, "cs.mention.scan", "scan mention", err)
	}
	return m, nil
}
