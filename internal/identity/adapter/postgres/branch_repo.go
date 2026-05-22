package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type BranchRepository struct {
	pool *pgxpool.Pool
}

func NewBranchRepository(pool *pgxpool.Pool) *BranchRepository {
	return &BranchRepository{pool: pool}
}

var _ port.BranchRepository = (*BranchRepository)(nil)

const branchColumns = `id, name, code, level, parent_id, active, created_at, updated_at`

func (r *BranchRepository) List(ctx context.Context) ([]domain.Branch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+branchColumns+`
		FROM identity.branches
		ORDER BY level, name
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.branch_list", "list branches", err)
	}
	defer rows.Close()

	out := []domain.Branch{}
	for rows.Next() {
		b, err := scanBranch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *BranchRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Branch, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+branchColumns+` FROM identity.branches WHERE id = $1`, id)
	b, err := scanBranch(row)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *BranchRepository) Create(ctx context.Context, b *domain.Branch) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity.branches (id, name, code, level, parent_id, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, b.ID, b.Name, b.Code, string(b.Level), b.ParentID, b.Active, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		if containsSQLState(err.Error(), "23505") {
			return derrors.Conflict("branch.code_taken", "branch code already in use")
		}
		return derrors.Wrap(derrors.KindInternal, "db.branch_insert", "insert branch", err)
	}
	return nil
}

func (r *BranchRepository) Update(ctx context.Context, b *domain.Branch) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE identity.branches
		SET name = $2, active = $3
		WHERE id = $1
	`, b.ID, b.Name, b.Active)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.branch_update", "update branch", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("branch.not_found", "branch not found")
	}
	return nil
}

func (r *BranchRepository) CountByLevel(ctx context.Context) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT level, COUNT(*) FROM identity.branches
		WHERE active = TRUE
		GROUP BY level
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.branch_count", "count branches", err)
	}
	defer rows.Close()
	out := map[string]int{"regional": 0, "area": 0, "sub_area": 0}
	for rows.Next() {
		var level string
		var n int
		if err := rows.Scan(&level, &n); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.branch_count_scan", "scan", err)
		}
		out[level] = n
	}
	return out, nil
}

// --- helpers ---

func scanBranch(scanner pgx.Row) (domain.Branch, error) {
	var (
		b     domain.Branch
		level string
	)
	err := scanner.Scan(&b.ID, &b.Name, &b.Code, &level, &b.ParentID, &b.Active, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Branch{}, derrors.NotFound("branch.not_found", "branch not found")
	}
	if err != nil {
		return domain.Branch{}, derrors.Wrap(derrors.KindInternal, "db.branch_scan", "scan branch", err)
	}
	b.Level = domain.BranchLevel(level)
	return b, nil
}
