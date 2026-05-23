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

// TeamRepository implements port.TeamRepository against cs.teams.
type TeamRepository struct {
	pool *pgxpool.Pool
}

func NewTeamRepository(pool *pgxpool.Pool) *TeamRepository {
	return &TeamRepository{pool: pool}
}

var _ port.TeamRepository = (*TeamRepository)(nil)

const teamCols = `
	id, name, COALESCE(description,''), manager_user_id, members_count,
	COALESCE(focus_ticket_types, '{}'), is_active,
	created_at, updated_at
`

func (r *TeamRepository) Insert(ctx context.Context, t *domain.Team) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs.teams
			(id, name, description, manager_user_id, members_count,
			 focus_ticket_types, is_active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`,
		t.ID, t.Name, nullableString(t.Description), t.ManagerUserID, t.MembersCount,
		ticketTypesToStringArray(t.FocusTicketTypes), t.IsActive, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.team", "insert team")
	}
	return nil
}

func (r *TeamRepository) Update(ctx context.Context, t *domain.Team) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.teams SET
			name               = $2,
			description        = $3,
			manager_user_id    = $4,
			members_count      = $5,
			focus_ticket_types = $6,
			is_active          = $7,
			updated_at         = NOW()
		WHERE id = $1
	`,
		t.ID, t.Name, nullableString(t.Description), t.ManagerUserID, t.MembersCount,
		ticketTypesToStringArray(t.FocusTicketTypes), t.IsActive,
	)
	if err != nil {
		return mapDBError(err, "cs.team", "update team")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.team.not_found", "team not found")
	}
	return nil
}

func (r *TeamRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+teamCols+` FROM cs.teams WHERE id = $1`, id)
	t, err := scanTeam(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TeamRepository) List(ctx context.Context, onlyActive bool) ([]domain.Team, error) {
	where := ""
	if onlyActive {
		where = ` WHERE is_active = TRUE`
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+teamCols+` FROM cs.teams`+where+` ORDER BY name ASC`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.team.list", "list teams", err)
	}
	defer rows.Close()
	out := []domain.Team{}
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func scanTeam(row pgx.Row) (domain.Team, error) {
	var t domain.Team
	var focusArr []string
	err := row.Scan(
		&t.ID, &t.Name, &t.Description, &t.ManagerUserID, &t.MembersCount,
		&focusArr, &t.IsActive,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Team{}, derrors.NotFound("cs.team.not_found", "team not found")
	}
	if err != nil {
		return domain.Team{}, derrors.Wrap(derrors.KindInternal, "cs.team.scan", "scan team", err)
	}
	t.FocusTicketTypes = stringArrayToTicketTypes(focusArr)
	return t, nil
}

func ticketTypesToStringArray(in []domain.TicketType) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, string(v))
	}
	return out
}

func stringArrayToTicketTypes(in []string) []domain.TicketType {
	out := make([]domain.TicketType, 0, len(in))
	for _, v := range in {
		out = append(out, domain.TicketType(v))
	}
	return out
}
