package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SchemaRepository is the pgx-backed implementation of
// port.SchemaRepository.
type SchemaRepository struct {
	pool *pgxpool.Pool
}

func NewSchemaRepository(pool *pgxpool.Pool) *SchemaRepository {
	return &SchemaRepository{pool: pool}
}

var _ port.SchemaRepository = (*SchemaRepository)(nil)

const schemaCols = `
	id, kind, code, version_no, name, description, body, status,
	published_at, superseded_at, notes, created_by, created_at, updated_at
`

func (r *SchemaRepository) Create(ctx context.Context, s *domain.SchemaDefinition) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO platform.schema_definitions
			(id, kind, code, version_no, name, description, body, status,
			 published_at, superseded_at, notes, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		s.ID, string(s.Kind), s.Code, s.VersionNo, s.Name, s.Description,
		[]byte(s.Body), string(s.Status), s.PublishedAt, s.SupersededAt,
		s.Notes, s.CreatedBy, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "schema", "insert schema")
	}
	return nil
}

func (r *SchemaRepository) Update(ctx context.Context, s *domain.SchemaDefinition) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE platform.schema_definitions
		SET name = $2, description = $3, body = $4, status = $5,
		    published_at = $6, superseded_at = $7, notes = $8,
		    updated_at = $9
		WHERE id = $1
	`,
		s.ID, s.Name, s.Description, []byte(s.Body), string(s.Status),
		s.PublishedAt, s.SupersededAt, s.Notes, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "schema", "update schema")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("schema.not_found", "schema not found")
	}
	return nil
}

func (r *SchemaRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+schemaCols+` FROM platform.schema_definitions WHERE id = $1`,
		id,
	)
	s, err := scanSchema(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SchemaRepository) FindLatestPublished(ctx context.Context, kind domain.SchemaKind, code string) (*domain.SchemaDefinition, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+schemaCols+`
		FROM platform.schema_definitions
		WHERE kind = $1 AND code = $2 AND status = 'published'
		ORDER BY version_no DESC
		LIMIT 1
	`,
		string(kind), code,
	)
	s, err := scanSchema(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SchemaRepository) MaxVersion(ctx context.Context, kind domain.SchemaKind, code string) (int, error) {
	var v *int
	err := r.pool.QueryRow(ctx, `
		SELECT MAX(version_no)
		FROM platform.schema_definitions
		WHERE kind = $1 AND code = $2
	`,
		string(kind), code,
	).Scan(&v)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "schema.max_version", "max version lookup", err)
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

func (r *SchemaRepository) List(ctx context.Context, f port.SchemaListFilter) ([]domain.SchemaDefinition, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var wh []string
	var args []any
	idx := 1
	if f.Kind != "" {
		wh = append(wh, fmt.Sprintf("kind = $%d", idx))
		args = append(args, string(f.Kind))
		idx++
	}
	if f.Status != "" {
		wh = append(wh, fmt.Sprintf("status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.Code != "" {
		wh = append(wh, fmt.Sprintf("code = $%d", idx))
		args = append(args, f.Code)
		idx++
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM platform.schema_definitions`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "schema.count", "count schemas", err)
	}

	limitIdx := idx
	offsetIdx := idx + 1
	args = append(args, limit, f.Offset)
	sql := `SELECT ` + schemaCols + ` FROM platform.schema_definitions` + where +
		` ORDER BY kind, code, version_no DESC` +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", limitIdx, offsetIdx)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "schema.list", "list schemas", err)
	}
	defer rows.Close()
	out := []domain.SchemaDefinition{}
	for rows.Next() {
		s, err := scanSchema(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func scanSchema(row pgx.Row) (domain.SchemaDefinition, error) {
	var (
		s        domain.SchemaDefinition
		kind     string
		status   string
		bodyRaw  []byte
	)
	err := row.Scan(
		&s.ID, &kind, &s.Code, &s.VersionNo, &s.Name, &s.Description,
		&bodyRaw, &status, &s.PublishedAt, &s.SupersededAt,
		&s.Notes, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SchemaDefinition{}, derrors.NotFound("schema.not_found", "schema not found")
	}
	if err != nil {
		return domain.SchemaDefinition{}, derrors.Wrap(derrors.KindInternal, "schema.scan", "scan schema", err)
	}
	s.Kind = domain.SchemaKind(kind)
	s.Status = domain.SchemaStatus(status)
	// Defensive copy — pgx may reuse the buffer between rows.
	if len(bodyRaw) > 0 {
		buf := make([]byte, len(bodyRaw))
		copy(buf, bodyRaw)
		s.Body = buf
	} else {
		s.Body = []byte("{}")
	}
	return s, nil
}
