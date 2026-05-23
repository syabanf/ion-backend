package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// AssignmentHistoryRepository implements port.AssignmentHistoryRepository
// against cs.ticket_assignments_history.
type AssignmentHistoryRepository struct {
	pool *pgxpool.Pool
}

func NewAssignmentHistoryRepository(pool *pgxpool.Pool) *AssignmentHistoryRepository {
	return &AssignmentHistoryRepository{pool: pool}
}

var _ port.AssignmentHistoryRepository = (*AssignmentHistoryRepository)(nil)

func (r *AssignmentHistoryRepository) Insert(ctx context.Context, ev *domain.AssignmentEvent) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs.ticket_assignments_history
			(id, ticket_id, assignment_kind,
			 from_user_id, to_user_id, from_team_id, to_team_id,
			 reason, assigned_by, assigned_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`,
		ev.ID, ev.TicketID, string(ev.AssignmentKind),
		ev.FromUserID, ev.ToUserID, ev.FromTeamID, ev.ToTeamID,
		nullableString(ev.Reason), ev.AssignedBy, ev.AssignedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.assign_history", "insert assignment history")
	}
	return nil
}

func (r *AssignmentHistoryRepository) ListByTicket(ctx context.Context, ticketID uuid.UUID, limit int) ([]domain.AssignmentEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, ticket_id, assignment_kind,
		       from_user_id, to_user_id, from_team_id, to_team_id,
		       COALESCE(reason,''), assigned_by, assigned_at
		  FROM cs.ticket_assignments_history
		 WHERE ticket_id = $1
		 ORDER BY assigned_at DESC
		 LIMIT $2
	`, ticketID, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.assign_history.list", "list assignment history", err)
	}
	defer rows.Close()
	out := []domain.AssignmentEvent{}
	for rows.Next() {
		var ev domain.AssignmentEvent
		var kind string
		if err := rows.Scan(
			&ev.ID, &ev.TicketID, &kind,
			&ev.FromUserID, &ev.ToUserID, &ev.FromTeamID, &ev.ToTeamID,
			&ev.Reason, &ev.AssignedBy, &ev.AssignedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "cs.assign_history.scan", "scan assignment history", err)
		}
		ev.AssignmentKind = domain.AssignmentKind(kind)
		out = append(out, ev)
	}
	return out, nil
}
