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

// CalendarEventRepository persists operations.calendar_events.
type CalendarEventRepository struct {
	pool *pgxpool.Pool
}

func NewCalendarEventRepository(pool *pgxpool.Pool) *CalendarEventRepository {
	return &CalendarEventRepository{pool: pool}
}

var _ port.CalendarEventRepository = (*CalendarEventRepository)(nil)

func (r *CalendarEventRepository) Create(ctx context.Context, e *domain.CalendarEvent) error {
	if e == nil {
		return derrors.Validation("calendar.nil", "event is nil")
	}
	metadataJSON, _ := json.Marshal(e.Metadata)
	if len(metadataJSON) == 0 {
		metadataJSON = []byte("{}")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO operations.calendar_events
			(id, event_kind, event_source, source_id, title, description,
			 scope, scope_id, all_day, starts_at, ends_at, color_hex,
			 metadata, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6,
		        $7, $8, $9, $10, $11, $12,
		        $13::jsonb, $14, $15, $16)
	`,
		e.ID, string(e.EventKind), string(e.EventSource), e.SourceID, e.Title, e.Description,
		string(e.Scope), e.ScopeID, e.AllDay, e.StartsAt, e.EndsAt, e.ColorHex,
		string(metadataJSON), e.CreatedBy, e.CreatedAt, e.UpdatedAt,
	)
	return mapDBError(err, "calendar", "insert event")
}

func (r *CalendarEventRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.CalendarEvent, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, event_kind, event_source, source_id, title,
		       COALESCE(description, ''), scope, scope_id, all_day,
		       starts_at, ends_at, COALESCE(color_hex, ''),
		       COALESCE(metadata::text, '{}'),
		       created_by, created_at, updated_at
		  FROM operations.calendar_events
		 WHERE id = $1
	`, id)
	e, err := scanCalendarRow(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (r *CalendarEventRepository) ListInRange(ctx context.Context, from, to time.Time, scope domain.EventScope, scopeID *uuid.UUID, limit int) ([]domain.CalendarEvent, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `
		SELECT id, event_kind, event_source, source_id, title,
		       COALESCE(description, ''), scope, scope_id, all_day,
		       starts_at, ends_at, COALESCE(color_hex, ''),
		       COALESCE(metadata::text, '{}'),
		       created_by, created_at, updated_at
		  FROM operations.calendar_events
		 WHERE starts_at <= $2
		   AND (ends_at IS NULL OR ends_at >= $1)`
	args := []any{from, to}
	if scope != "" && scope != domain.ScopeGlobal {
		q += ` AND (scope = 'global' OR (scope = $3 AND ($4::uuid IS NULL OR scope_id = $4)))`
		args = append(args, string(scope), scopeID)
	} else {
		q += ` AND scope = 'global'`
	}
	q += ` ORDER BY starts_at ASC LIMIT ` + intLimitArg(limit)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapDBError(err, "calendar", "list in range")
	}
	defer rows.Close()
	out := []domain.CalendarEvent{}
	for rows.Next() {
		e, err := scanCalendarRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *CalendarEventRepository) UpsertBySource(ctx context.Context, e *domain.CalendarEvent) error {
	if e == nil || e.SourceID == nil {
		// Manual entries go through Create; only auto-sync uses this path.
		return derrors.Validation("calendar.source_required", "source_id is required for UpsertBySource")
	}
	metadataJSON, _ := json.Marshal(e.Metadata)
	if len(metadataJSON) == 0 {
		metadataJSON = []byte("{}")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO operations.calendar_events
			(id, event_kind, event_source, source_id, title, description,
			 scope, scope_id, all_day, starts_at, ends_at, color_hex,
			 metadata, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6,
		        $7, $8, $9, $10, $11, $12,
		        $13::jsonb, $14, NOW(), NOW())
		ON CONFLICT (event_source, source_id)
		WHERE source_id IS NOT NULL
		DO UPDATE SET
			event_kind  = EXCLUDED.event_kind,
			title       = EXCLUDED.title,
			description = EXCLUDED.description,
			scope       = EXCLUDED.scope,
			scope_id    = EXCLUDED.scope_id,
			all_day     = EXCLUDED.all_day,
			starts_at   = EXCLUDED.starts_at,
			ends_at     = EXCLUDED.ends_at,
			color_hex   = EXCLUDED.color_hex,
			metadata    = EXCLUDED.metadata,
			updated_at  = NOW()
	`,
		e.ID, string(e.EventKind), string(e.EventSource), e.SourceID, e.Title, e.Description,
		string(e.Scope), e.ScopeID, e.AllDay, e.StartsAt, e.EndsAt, e.ColorHex,
		string(metadataJSON), e.CreatedBy,
	)
	return mapDBError(err, "calendar", "upsert by source")
}

func (r *CalendarEventRepository) Update(ctx context.Context, e *domain.CalendarEvent) error {
	if e == nil {
		return derrors.Validation("calendar.nil", "event is nil")
	}
	metadataJSON, _ := json.Marshal(e.Metadata)
	if len(metadataJSON) == 0 {
		metadataJSON = []byte("{}")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.calendar_events
		   SET event_kind  = $2,
		       title       = $3,
		       description = $4,
		       scope       = $5,
		       scope_id    = $6,
		       all_day     = $7,
		       starts_at   = $8,
		       ends_at     = $9,
		       color_hex   = $10,
		       metadata    = $11::jsonb,
		       updated_at  = NOW()
		 WHERE id = $1
	`,
		e.ID, string(e.EventKind), e.Title, e.Description,
		string(e.Scope), e.ScopeID, e.AllDay, e.StartsAt, e.EndsAt, e.ColorHex,
		string(metadataJSON),
	)
	if err != nil {
		return mapDBError(err, "calendar", "update event")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("calendar.not_found", "calendar event not found")
	}
	return nil
}

func scanCalendarRow(row rowScanner) (*domain.CalendarEvent, error) {
	var e domain.CalendarEvent
	var kind, source, scope, metadataJSON string
	err := row.Scan(
		&e.ID, &kind, &source, &e.SourceID, &e.Title, &e.Description,
		&scope, &e.ScopeID, &e.AllDay,
		&e.StartsAt, &e.EndsAt, &e.ColorHex, &metadataJSON,
		&e.CreatedBy, &e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	e.EventKind = domain.NormalizeEventKind(kind)
	e.EventSource = domain.EventSource(source)
	e.Scope = domain.NormalizeScope(scope)
	if metadataJSON != "" && metadataJSON != "{}" {
		_ = json.Unmarshal([]byte(metadataJSON), &e.Metadata)
	}
	return &e, nil
}

// intLimitArg renders a sanitized int constant for the LIMIT clause.
// We avoid binding LIMIT as a parameter because Postgres needs a positive
// integer literal — and we've already clamped the value.
func intLimitArg(n int) string {
	if n <= 0 {
		n = 500
	}
	if n > 5000 {
		n = 5000
	}
	// strconv-free formatting to keep this file dependency-light.
	return itoaShort(n)
}

func itoaShort(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [10]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + (n % 10))
		n /= 10
	}
	return string(buf[i:])
}
