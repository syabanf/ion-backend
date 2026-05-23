package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 103 — Technician mobile EWO repository
//
// All methods enforce side='y' AND assigned_technician_user_id = $tech.
// This is the load-bearing scope rule: a technician must never see an
// EWO-X (commercial side) and must never see an EWO-Y assigned to
// someone else. The constraint lives in the SQL — handlers can be
// reorganised without weakening it.
// =====================================================================

type EWOMobileRepository struct {
	pool *pgxpool.Pool
}

func NewEWOMobileRepository(pool *pgxpool.Pool) *EWOMobileRepository {
	return &EWOMobileRepository{pool: pool}
}

var _ port.EWOMobileRepository = (*EWOMobileRepository)(nil)

// AssignedToTechnician runs the dashboard query. The (technician,
// scheduled_start) index from migration 0065 covers this access path.
func (r *EWOMobileRepository) AssignedToTechnician(
	ctx context.Context,
	technicianID uuid.UUID,
	filter port.EWOMobileFilter,
) ([]domain.EWO, error) {
	if technicianID == uuid.Nil {
		return nil, derrors.Validation(
			"ewo_mobile.technician_required",
			"technician_id is required",
		)
	}
	args := []any{technicianID}
	conds := []string{
		"assigned_technician_user_id = $1",
		"side = 'y'",
	}
	if len(filter.StatusIn) > 0 {
		statuses := make([]string, 0, len(filter.StatusIn))
		for _, s := range filter.StatusIn {
			statuses = append(statuses, string(s))
		}
		args = append(args, statuses)
		conds = append(conds, fmt.Sprintf("status = ANY($%d::text[])", len(args)))
	}
	if filter.From != nil {
		args = append(args, *filter.From)
		conds = append(conds, fmt.Sprintf("scheduled_end_date >= $%d", len(args)))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		conds = append(conds, fmt.Sprintf("scheduled_start_date <= $%d", len(args)))
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + ewoCols + `
		FROM enterprise.ewos
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY COALESCE(scheduled_start_date, created_at) ASC
		LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"db.ewo_mobile_list", "list assigned ewos", err)
	}
	defer rows.Close()
	out := []domain.EWO{}
	for rows.Next() {
		e, err := scanEWO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// GetForTechnician returns the EWO IF assigned to the technician. The
// "is it assigned to you" predicate is part of the WHERE clause, so a
// cross-technician access hits the same code path as a non-existent
// EWO — both return NotFound. This is the deliberate 404-not-403
// semantics required by the wave: 403 would leak "yes this EWO id
// exists but you can't see it"; 404 is opaque.
func (r *EWOMobileRepository) GetForTechnician(
	ctx context.Context,
	technicianID, ewoID uuid.UUID,
) (*domain.EWO, error) {
	if technicianID == uuid.Nil {
		return nil, derrors.Validation(
			"ewo_mobile.technician_required",
			"technician_id is required",
		)
	}
	row := r.pool.QueryRow(ctx, `SELECT `+ewoCols+`
		FROM enterprise.ewos
		WHERE id = $1
		  AND assigned_technician_user_id = $2
		  AND side = 'y'`, ewoID, technicianID)
	e, err := scanEWO(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
