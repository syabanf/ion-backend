package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// EWOScheduleHistoryRepository persists reschedule audit rows
// (Wave 96 / migration 0065). Append-only: there's no Update or
// Delete method. Each call to Create writes one row capturing the
// pre-change values, so a reviewer can walk the schedule timeline
// in forward order.
type EWOScheduleHistoryRepository struct {
	pool *pgxpool.Pool
}

func NewEWOScheduleHistoryRepository(pool *pgxpool.Pool) *EWOScheduleHistoryRepository {
	return &EWOScheduleHistoryRepository{pool: pool}
}

var _ port.EWOScheduleHistoryRepository = (*EWOScheduleHistoryRepository)(nil)

const ewoScheduleHistoryCols = `
	id, ewo_id, prev_start, prev_end,
	prev_team_lead, prev_technician,
	changed_by, changed_at, COALESCE(reason, '')
`

// Create writes one history row. uuid.Nil sentinel values on team_lead
// or technician are stored as NULLs so a reviewer can distinguish
// "was unassigned" from "had user 00000000".
func (r *EWOScheduleHistoryRepository) Create(
	ctx context.Context,
	entry *domain.ScheduleHistoryEntry,
) error {
	var (
		prevTL  any
		prevTec any
	)
	if entry.PrevTeamLead != uuid.Nil {
		prevTL = entry.PrevTeamLead
	}
	if entry.PrevTechnician != uuid.Nil {
		prevTec = entry.PrevTechnician
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.ewo_schedule_history
			(id, ewo_id, prev_start, prev_end,
			 prev_team_lead, prev_technician,
			 changed_by, changed_at, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		entry.ID, entry.EWOID, entry.PrevStart, entry.PrevEnd,
		prevTL, prevTec,
		entry.ChangedBy, entry.ChangedAt, entry.Reason,
	)
	if err != nil {
		return mapDBError(err, "ewo_schedule_history", "insert reschedule audit")
	}
	return nil
}

// ListByEWO returns the reschedule history rows for one EWO, newest
// first. Bounded at 200 rows — even an aggressively-rescheduled work
// order shouldn't exceed that, and a hard cap keeps the response
// payload predictable.
func (r *EWOScheduleHistoryRepository) ListByEWO(
	ctx context.Context,
	ewoID uuid.UUID,
) ([]domain.ScheduleHistoryEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+ewoScheduleHistoryCols+`
		FROM enterprise.ewo_schedule_history
		WHERE ewo_id = $1
		ORDER BY changed_at DESC
		LIMIT 200
	`, ewoID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.ewo_schedule_history_list", "list", err)
	}
	defer rows.Close()
	out := []domain.ScheduleHistoryEntry{}
	for rows.Next() {
		var (
			e       domain.ScheduleHistoryEntry
			prevTL  *uuid.UUID
			prevTec *uuid.UUID
		)
		if err := rows.Scan(
			&e.ID, &e.EWOID, &e.PrevStart, &e.PrevEnd,
			&prevTL, &prevTec,
			&e.ChangedBy, &e.ChangedAt, &e.Reason,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.ewo_schedule_history_scan", "scan", err)
		}
		if prevTL != nil {
			e.PrevTeamLead = *prevTL
		}
		if prevTec != nil {
			e.PrevTechnician = *prevTec
		}
		out = append(out, e)
	}
	return out, nil
}
