package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// EventRepository implements port.EventRepository against
// `hris.employee_events`.
type EventRepository struct {
	pool *pgxpool.Pool
}

func NewEventRepository(pool *pgxpool.Pool) *EventRepository {
	return &EventRepository{pool: pool}
}

var _ port.EventRepository = (*EventRepository)(nil)

const eventCols = `
	id, employee_no, event_kind,
	COALESCE(event_payload, '{}'::jsonb),
	occurred_at, ingested_at, COALESCE(source, 'manual'),
	processed, processed_at, COALESCE(processing_error, '')
`

// CreateMany inserts a batch of events. Idempotent on id — re-insert is
// a no-op via ON CONFLICT (id) DO NOTHING. Returns the count of rows
// actually inserted (i.e. previously-unseen ids).
func (r *EventRepository) CreateMany(ctx context.Context, events []*domain.EmployeeEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, mapDBError(err, "hris.event", "begin tx")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted := 0
	for _, ev := range events {
		payload, perr := json.Marshal(ev.Payload)
		if perr != nil {
			payload = []byte("{}")
		}
		ct, err := tx.Exec(ctx, `
			INSERT INTO hris.employee_events
				(id, employee_no, event_kind, event_payload,
				 occurred_at, ingested_at, source, processed, processed_at, processing_error)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (id) DO NOTHING
		`,
			ev.ID, ev.EmployeeNo, string(ev.Kind), payload,
			ev.OccurredAt, ev.IngestedAt, ev.Source,
			ev.Processed, ev.ProcessedAt, nullableString(ev.ProcessingError),
		)
		if err != nil {
			return inserted, mapDBError(err, "hris.event", "insert event")
		}
		if ct.RowsAffected() > 0 {
			inserted++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return inserted, mapDBError(err, "hris.event", "commit tx")
	}
	return inserted, nil
}

func (r *EventRepository) ListPending(ctx context.Context, limit int) ([]domain.EmployeeEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+eventCols+`
		   FROM hris.employee_events
		  WHERE processed = FALSE
		  ORDER BY ingested_at ASC
		  LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, mapDBError(err, "hris.event", "list pending")
	}
	defer rows.Close()
	var out []domain.EmployeeEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (r *EventRepository) List(ctx context.Context, f port.EventFilter) ([]domain.EmployeeEvent, int, error) {
	var wh []string
	var args []any
	if f.EmployeeNo != "" {
		args = append(args, f.EmployeeNo)
		wh = append(wh, fmt.Sprintf("employee_no = $%d", len(args)))
	}
	if f.Kind != "" {
		args = append(args, string(f.Kind))
		wh = append(wh, fmt.Sprintf("event_kind = $%d", len(args)))
	}
	if f.Processed != nil {
		args = append(args, *f.Processed)
		wh = append(wh, fmt.Sprintf("processed = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit)
	limitIdx := len(args)
	args = append(args, offset)
	offsetIdx := len(args)
	query := `SELECT ` + eventCols + ` FROM hris.employee_events` + where +
		` ORDER BY occurred_at DESC` +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", limitIdx, offsetIdx)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, mapDBError(err, "hris.event", "list events")
	}
	defer rows.Close()
	var out []domain.EmployeeEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapDBError(err, "hris.event", "iterate events")
	}
	var total int
	countArgs := args[:len(args)-2]
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM hris.employee_events"+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, mapDBError(err, "hris.event", "count events")
	}
	return out, total, nil
}

// MarkProcessed flips processed=true + sets processed_at=NOW(). If
// processingError is non-empty, it is stored verbatim.
func (r *EventRepository) MarkProcessed(ctx context.Context, id uuid.UUID, processingError string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE hris.employee_events
		   SET processed = TRUE,
		       processed_at = NOW(),
		       processing_error = $2
		 WHERE id = $1
	`, id, nullableString(processingError))
	if err != nil {
		return mapDBError(err, "hris.event", "mark processed")
	}
	return nil
}

func scanEvent(r scanRow) (domain.EmployeeEvent, error) {
	var ev domain.EmployeeEvent
	var kind string
	var payload []byte
	var processedAt *time.Time
	if err := r.Scan(
		&ev.ID, &ev.EmployeeNo, &kind,
		&payload,
		&ev.OccurredAt, &ev.IngestedAt, &ev.Source,
		&ev.Processed, &processedAt, &ev.ProcessingError,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ev, err
		}
		return ev, derrors.Wrap(derrors.KindInternal, "hris.event", "scan event row", err)
	}
	ev.Kind = domain.EventKind(kind)
	if len(payload) > 0 {
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err == nil {
			ev.Payload = m
		}
	}
	if ev.Payload == nil {
		ev.Payload = map[string]any{}
	}
	ev.ProcessedAt = processedAt
	return ev, nil
}
