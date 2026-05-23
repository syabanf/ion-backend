package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// TeamMemberRepository implements port.TeamMemberRepository.
type TeamMemberRepository struct {
	pool *pgxpool.Pool
}

func NewTeamMemberRepository(pool *pgxpool.Pool) *TeamMemberRepository {
	return &TeamMemberRepository{pool: pool}
}

var _ port.TeamMemberRepository = (*TeamMemberRepository)(nil)

const teamMemberCols = `id, team_id, user_id, role_in_team, joined_at, left_at`

func (r *TeamMemberRepository) Insert(ctx context.Context, m *domain.TeamMember) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs.team_members
			(id, team_id, user_id, role_in_team, joined_at, left_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`,
		m.ID, m.TeamID, m.UserID, string(m.RoleInTeam), m.JoinedAt, m.LeftAt,
	)
	if err != nil {
		return mapDBError(err, "cs.team_member", "insert team member")
	}
	return nil
}

func (r *TeamMemberRepository) Update(ctx context.Context, m *domain.TeamMember) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.team_members SET
			role_in_team = $2,
			left_at      = $3
		WHERE id = $1
	`, m.ID, string(m.RoleInTeam), m.LeftAt)
	if err != nil {
		return mapDBError(err, "cs.team_member", "update team member")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.team_member.not_found", "team member not found")
	}
	return nil
}

func (r *TeamMemberRepository) FindActiveByTeamUser(ctx context.Context, teamID, userID uuid.UUID) (*domain.TeamMember, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+teamMemberCols+`
		  FROM cs.team_members
		 WHERE team_id = $1
		   AND user_id = $2
		   AND left_at IS NULL
	`, teamID, userID)
	m, err := scanTeamMember(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *TeamMemberRepository) ListByTeam(ctx context.Context, teamID uuid.UUID, includeLeft bool) ([]domain.TeamMember, error) {
	q := `SELECT ` + teamMemberCols + ` FROM cs.team_members WHERE team_id = $1`
	if !includeLeft {
		q += ` AND left_at IS NULL`
	}
	q += ` ORDER BY joined_at ASC`
	rows, err := r.pool.Query(ctx, q, teamID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.team_member.list", "list team members", err)
	}
	defer rows.Close()
	out := []domain.TeamMember{}
	for rows.Next() {
		m, err := scanTeamMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// OpenTicketCountByUser counts non-terminal tickets per assigned user
// for the given team. Used by round-robin assignment to pick the
// member with the lowest queue.
func (r *TeamMemberRepository) OpenTicketCountByUser(ctx context.Context, teamID uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tm.user_id, COUNT(t.id)
		  FROM cs.team_members tm
		  LEFT JOIN cs.tickets t
		    ON t.assigned_user_id = tm.user_id
		   AND t.status NOT IN ('resolved','closed')
		 WHERE tm.team_id = $1
		   AND tm.left_at IS NULL
		 GROUP BY tm.user_id
	`, teamID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.team_member.count_open", "count open tickets per user", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]int)
	for rows.Next() {
		var uid uuid.UUID
		var cnt int
		if err := rows.Scan(&uid, &cnt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "cs.team_member.scan_open", "scan open count", err)
		}
		out[uid] = cnt
	}
	return out, nil
}

func scanTeamMember(row pgx.Row) (domain.TeamMember, error) {
	var m domain.TeamMember
	var role string
	err := row.Scan(&m.ID, &m.TeamID, &m.UserID, &role, &m.JoinedAt, &m.LeftAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TeamMember{}, derrors.NotFound("cs.team_member.not_found", "team member not found")
	}
	if err != nil {
		return domain.TeamMember{}, derrors.Wrap(derrors.KindInternal, "cs.team_member.scan", "scan team member", err)
	}
	m.RoleInTeam = domain.TeamMemberRole(role)
	return m, nil
}
