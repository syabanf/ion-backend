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

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// FaultEventRepository implements port.FaultEventRepository against
// `nocmon.fault_events`.
type FaultEventRepository struct {
	pool *pgxpool.Pool
}

func NewFaultEventRepository(pool *pgxpool.Pool) *FaultEventRepository {
	return &FaultEventRepository{pool: pool}
}

var _ port.FaultEventRepository = (*FaultEventRepository)(nil)

const faultCols = `
	id, kind, severity, source_id, COALESCE(source_kind, ''),
	started_at, detected_at,
	acknowledged_at, acknowledged_by,
	resolved_at, resolved_by,
	COALESCE(root_cause, ''),
	customer_impact_count, status, ticket_wo_id,
	created_at, updated_at
`

func (r *FaultEventRepository) Create(ctx context.Context, f *domain.FaultEvent) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO nocmon.fault_events
			(id, kind, severity, source_id, source_kind,
			 started_at, detected_at,
			 acknowledged_at, acknowledged_by,
			 resolved_at, resolved_by,
			 root_cause, customer_impact_count, status, ticket_wo_id,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`,
		f.ID, string(f.Kind), string(f.Severity), f.SourceID, nullableString(f.SourceKind),
		f.StartedAt, f.DetectedAt,
		f.AcknowledgedAt, f.AcknowledgedBy,
		f.ResolvedAt, f.ResolvedBy,
		nullableString(f.RootCause), f.CustomerImpactCount, string(f.Status), f.TicketWOID,
		f.CreatedAt, f.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "fault", "insert fault_event")
	}
	return nil
}

func (r *FaultEventRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.FaultEvent, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+faultCols+` FROM nocmon.fault_events WHERE id = $1`, id)
	f, err := scanFault(row)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (r *FaultEventRepository) List(ctx context.Context, f port.FaultListFilter) ([]domain.FaultEvent, int, error) {
	args := []any{}
	wh := []string{}
	if f.Status != "" {
		args = append(args, string(f.Status))
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.Severity != "" {
		args = append(args, string(f.Severity))
		wh = append(wh, fmt.Sprintf("severity = $%d", len(args)))
	}
	if f.Kind != "" {
		args = append(args, string(f.Kind))
		wh = append(wh, fmt.Sprintf("kind = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM nocmon.fault_events`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.fault_count", "count faults", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + faultCols + ` FROM nocmon.fault_events` + where +
		` ORDER BY started_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.fault_list", "list faults", err)
	}
	defer rows.Close()
	out := []domain.FaultEvent{}
	for rows.Next() {
		ev, err := scanFault(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ev)
	}
	return out, total, nil
}

// UpdateStatus is the single mutation path for the state machine. It
// rewrites every status-adjacent field so the caller can drive any
// transition (Acknowledge / Investigate / Mitigate / Resolve /
// MarkDuplicate / convert-to-WO) through the domain methods and
// then persist with a single repo call.
func (r *FaultEventRepository) UpdateStatus(ctx context.Context, f *domain.FaultEvent) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE nocmon.fault_events
		SET status = $2,
		    acknowledged_at = $3,
		    acknowledged_by = $4,
		    resolved_at = $5,
		    resolved_by = $6,
		    root_cause = $7,
		    customer_impact_count = $8,
		    ticket_wo_id = $9,
		    updated_at = $10
		WHERE id = $1
	`,
		f.ID, string(f.Status),
		f.AcknowledgedAt, f.AcknowledgedBy,
		f.ResolvedAt, f.ResolvedBy,
		nullableString(f.RootCause), f.CustomerImpactCount, f.TicketWOID,
		f.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "fault", "update fault_event")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("fault.not_found", "fault not found")
	}
	return nil
}

func (r *FaultEventRepository) ListOpenUnacked(ctx context.Context, olderThan time.Time, limit int) ([]domain.FaultEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+faultCols+`
		FROM nocmon.fault_events
		WHERE status = 'open'
		  AND acknowledged_at IS NULL
		  AND started_at < $1
		ORDER BY started_at ASC
		LIMIT $2
	`, olderThan, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.fault_unacked", "list unacked faults", err)
	}
	defer rows.Close()
	out := []domain.FaultEvent{}
	for rows.Next() {
		ev, err := scanFault(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func scanFault(row pgx.Row) (domain.FaultEvent, error) {
	var f domain.FaultEvent
	var kind, severity, status string
	err := row.Scan(
		&f.ID, &kind, &severity, &f.SourceID, &f.SourceKind,
		&f.StartedAt, &f.DetectedAt,
		&f.AcknowledgedAt, &f.AcknowledgedBy,
		&f.ResolvedAt, &f.ResolvedBy,
		&f.RootCause,
		&f.CustomerImpactCount, &status, &f.TicketWOID,
		&f.CreatedAt, &f.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FaultEvent{}, derrors.NotFound("fault.not_found", "fault not found")
	}
	if err != nil {
		return domain.FaultEvent{}, derrors.Wrap(derrors.KindInternal, "db.fault_scan", "scan fault", err)
	}
	f.Kind = domain.FaultKind(kind)
	f.Severity = domain.FaultSeverity(severity)
	f.Status = domain.FaultStatus(status)
	return f, nil
}
