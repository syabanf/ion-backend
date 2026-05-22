package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type TeamRepository struct {
	pool *pgxpool.Pool
}

func NewTeamRepository(pool *pgxpool.Pool) *TeamRepository {
	return &TeamRepository{pool: pool}
}

var _ port.TeamRepository = (*TeamRepository)(nil)

func (r *TeamRepository) Create(ctx context.Context, t *domain.Team) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO field.teams (id, code, name, branch_id, team_leader_id, active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
	`, t.ID, t.Code, t.Name, t.BranchID, t.TeamLeaderID, t.Active, t.CreatedAt)
	return mapDBError(err, "team.create", "create team")
}

const teamSelect = `
SELECT t.id, t.code, t.name, t.branch_id, t.team_leader_id, t.active, t.created_at, t.updated_at,
       COALESCE(b.name,''), COALESCE(b.code,''), COALESCE(u.full_name,''),
       (SELECT COUNT(*) FROM field.team_members m WHERE m.team_id = t.id AND m.active) AS member_count
FROM field.teams t
LEFT JOIN identity.branches b ON b.id = t.branch_id
LEFT JOIN identity.users u    ON u.id = t.team_leader_id
`

func (r *TeamRepository) List(ctx context.Context, branchID *uuid.UUID, activeOnly bool) ([]port.TeamView, error) {
	var args []any
	where := ""
	if activeOnly {
		where = " WHERE t.active"
	}
	if branchID != nil {
		args = append(args, *branchID)
		if where == "" {
			where = " WHERE t.branch_id = $1"
		} else {
			where += " AND t.branch_id = $1"
		}
	}
	rows, err := r.pool.Query(ctx, teamSelect+where+" ORDER BY t.name", args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.team_list", "list teams", err)
	}
	defer rows.Close()
	out := []port.TeamView{}
	for rows.Next() {
		v, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func (r *TeamRepository) FindByID(ctx context.Context, id uuid.UUID) (*port.TeamView, error) {
	row := r.pool.QueryRow(ctx, teamSelect+" WHERE t.id = $1", id)
	return scanTeam(row)
}

func (r *TeamRepository) AddMember(ctx context.Context, m *domain.TeamMember) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO field.team_members (id, team_id, user_id, grade, active, joined_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, m.ID, m.TeamID, m.UserID, string(m.Grade), m.Active, m.JoinedAt)
	return mapDBError(err, "team_member.add", "add team member")
}

func (r *TeamRepository) ListMembers(ctx context.Context, teamID uuid.UUID) ([]port.TeamMemberView, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.id, m.team_id, m.user_id, m.grade, m.active, m.joined_at,
		       COALESCE(u.full_name,''), COALESCE(u.email,'')
		FROM field.team_members m
		LEFT JOIN identity.users u ON u.id = m.user_id
		WHERE m.team_id = $1
		ORDER BY m.grade DESC, u.full_name
	`, teamID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.team_members_list", "list team members", err)
	}
	defer rows.Close()
	out := []port.TeamMemberView{}
	for rows.Next() {
		var (
			m     domain.TeamMember
			grade string
			v     port.TeamMemberView
		)
		if err := rows.Scan(&m.ID, &m.TeamID, &m.UserID, &grade, &m.Active, &m.JoinedAt,
			&v.Name, &v.Email); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.member_scan", "scan member", err)
		}
		m.Grade = domain.TechGrade(grade)
		v.Member = m
		out = append(out, v)
	}
	return out, nil
}

func scanTeam(row pgx.Row) (*port.TeamView, error) {
	var (
		t domain.Team
		v port.TeamView
	)
	err := row.Scan(&t.ID, &t.Code, &t.Name, &t.BranchID, &t.TeamLeaderID,
		&t.Active, &t.CreatedAt, &t.UpdatedAt,
		&v.BranchName, &v.BranchCode, &v.TeamLeaderName, &v.MemberCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("team.not_found", "team not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.team_scan", "scan team", err)
	}
	v.Team = t
	return &v, nil
}
