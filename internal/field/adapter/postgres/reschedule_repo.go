package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type RescheduleRepository struct {
	pool *pgxpool.Pool
}

func NewRescheduleRepository(pool *pgxpool.Pool) *RescheduleRepository {
	return &RescheduleRepository{pool: pool}
}

var _ port.RescheduleRepository = (*RescheduleRepository)(nil)

func (r *RescheduleRepository) Create(ctx context.Context, rs *domain.Reschedule) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO field.wo_reschedules
		  (id, wo_id, reason, notes, original_date, new_date, rescheduled_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		rs.ID, rs.WOID, string(rs.Reason), nullableString(rs.Notes),
		rs.OriginalDate, rs.NewDate, rs.RescheduledBy, rs.CreatedAt,
	)
	return mapDBError(err, "wo.reschedule.create", "create reschedule")
}

func (r *RescheduleRepository) ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.Reschedule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, wo_id, reason, COALESCE(notes,''), original_date, new_date,
		       rescheduled_by, created_at
		FROM field.wo_reschedules
		WHERE wo_id = $1
		ORDER BY created_at DESC
	`, woID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.reschedule_list", "list reschedules", err)
	}
	defer rows.Close()
	out := []domain.Reschedule{}
	for rows.Next() {
		var (
			rs     domain.Reschedule
			reason string
		)
		if err := rows.Scan(&rs.ID, &rs.WOID, &reason, &rs.Notes,
			&rs.OriginalDate, &rs.NewDate, &rs.RescheduledBy, &rs.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.reschedule_scan", "scan reschedule", err)
		}
		rs.Reason = domain.RescheduleReason(reason)
		out = append(out, rs)
	}
	return out, nil
}

// ListSLABreaches returns open WOs whose sla_due_at is past NOW().
// Uses the partial index idx_wo_sla_open for selectivity.
func (r *RescheduleRepository) ListSLABreaches(ctx context.Context, limit, offset int) ([]port.WODetail, int, error) {
	if limit <= 0 {
		limit = 50
	}
	const where = `
		WHERE w.sla_due_at IS NOT NULL
		  AND w.sla_due_at < NOW()
		  AND w.status NOT IN ('completed','cancelled')
	`
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM field.work_orders w"+where,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.sla_count", "count sla breaches", err)
	}
	rows, err := r.pool.Query(ctx,
		woSelect+where+" ORDER BY w.sla_due_at ASC LIMIT $1 OFFSET $2",
		limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.sla_list", "list sla breaches", err)
	}
	defer rows.Close()
	out := []port.WODetail{}
	for rows.Next() {
		d, err := scanWOHeader(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	return out, total, nil
}
