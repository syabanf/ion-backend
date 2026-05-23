package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// TicketCommentRepository implements port.TicketCommentRepository
// against cs.ticket_comments.
type TicketCommentRepository struct {
	pool *pgxpool.Pool
}

func NewTicketCommentRepository(pool *pgxpool.Pool) *TicketCommentRepository {
	return &TicketCommentRepository{pool: pool}
}

var _ port.TicketCommentRepository = (*TicketCommentRepository)(nil)

const commentCols = `
	id, ticket_id, author_id, COALESCE(author_role, ''),
	body, is_internal, COALESCE(attachments, '[]'::jsonb),
	created_at, updated_at, edited_at, deleted_at
`

func (r *TicketCommentRepository) Insert(ctx context.Context, c *domain.Comment) error {
	attBytes, err := json.Marshal(c.Attachments)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.comment.marshal", "marshal attachments", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.ticket_comments
			(id, ticket_id, author_id, author_role, body, is_internal, attachments,
			 created_at, updated_at, edited_at, deleted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`,
		c.ID, c.TicketID, c.AuthorID, nullableString(c.AuthorRole),
		c.Body, c.IsInternal, attBytes,
		c.CreatedAt, c.UpdatedAt, c.EditedAt, c.DeletedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.comment", "insert comment")
	}
	return nil
}

func (r *TicketCommentRepository) Update(ctx context.Context, c *domain.Comment) error {
	attBytes, err := json.Marshal(c.Attachments)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.comment.marshal", "marshal attachments", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.ticket_comments SET
			body = $2,
			is_internal = $3,
			attachments = $4,
			updated_at = NOW(),
			edited_at = $5,
			deleted_at = $6
		WHERE id = $1
	`,
		c.ID, c.Body, c.IsInternal, attBytes, c.EditedAt, c.DeletedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.comment", "update comment")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.comment.not_found", "comment not found")
	}
	return nil
}

func (r *TicketCommentRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Comment, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+commentCols+` FROM cs.ticket_comments WHERE id = $1`, id)
	c, err := scanComment(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *TicketCommentRepository) List(ctx context.Context, f port.CommentListFilter) ([]domain.Comment, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	where := "ticket_id = $1"
	args := []any{f.TicketID}
	if !f.IncludeDeleted {
		where += " AND deleted_at IS NULL"
	}
	if !f.IncludeInternal {
		where += " AND is_internal = FALSE"
	}
	// The WHERE clause may add static `AND deleted_at IS NULL` /
	// `AND is_internal = FALSE` predicates; they don't add positional
	// args, so the placeholders are always $1 ticket_id, $2 limit,
	// $3 offset.
	args = append(args, limit, offset)
	sql := `SELECT ` + commentCols + ` FROM cs.ticket_comments WHERE ` + where +
		` ORDER BY created_at ASC LIMIT $2 OFFSET $3`
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.comment.list", "list comments", err)
	}
	defer rows.Close()
	out := []domain.Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func scanComment(row pgx.Row) (domain.Comment, error) {
	var c domain.Comment
	var att []byte
	err := row.Scan(
		&c.ID, &c.TicketID, &c.AuthorID, &c.AuthorRole,
		&c.Body, &c.IsInternal, &att,
		&c.CreatedAt, &c.UpdatedAt, &c.EditedAt, &c.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Comment{}, derrors.NotFound("cs.comment.not_found", "comment not found")
	}
	if err != nil {
		return domain.Comment{}, derrors.Wrap(derrors.KindInternal, "cs.comment.scan", "scan comment", err)
	}
	if len(att) > 0 {
		_ = json.Unmarshal(att, &c.Attachments)
	}
	return c, nil
}
