// Package postgres holds the driven adapters that persist identity entities
// in PostgreSQL. The usecase layer depends on the interfaces in port/, not
// on this package.
//
// Conventions:
//   - SQL is hand-written (we'll move to sqlc once query volume justifies it).
//   - Driver errors are translated to pkg/errors before returning.
//   - All writes accept a context — never use context.Background() here.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Compile-time check.
var _ port.UserRepository = (*UserRepository)(nil)

const insertUserSQL = `
INSERT INTO identity.users (
    id, employee_id, full_name, email, phone, password_hash,
    reports_to_user_id, branch_id, branch_level, active, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
`

// Create inserts a user, assigns the given roles, and seeds the extension
// tables (sales_rep_profiles / technician_profiles) when the corresponding
// metadata is supplied. All in a single transaction so a partial failure
// leaves no orphan rows behind.
func (r *UserRepository) Create(
	ctx context.Context,
	u *domain.User,
	roleNames []string,
	salesType *domain.SalesType,
	techGrade *domain.TechnicianGrade,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var level *string
	if u.BranchLevel != nil {
		v := string(*u.BranchLevel)
		level = &v
	}

	if _, err := tx.Exec(ctx, insertUserSQL,
		u.ID, u.EmployeeID, u.FullName, u.Email, u.Phone, u.PasswordHash,
		u.ReportsToID, u.BranchID, level, u.Active, u.CreatedAt, u.UpdatedAt,
	); err != nil {
		return mapInsertError(err)
	}

	for _, name := range roleNames {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.user_roles (user_id, role_id, assigned_at)
			SELECT $1, id, NOW() FROM identity.roles WHERE name = $2
		`, u.ID, name); err != nil {
			return derrors.Wrap(derrors.KindInternal, "db.role_assign", "assign role", err)
		}
	}

	if salesType != nil {
		if _, err := tx.Exec(ctx,
			`INSERT INTO identity.sales_rep_profiles (user_id, sales_type) VALUES ($1, $2)`,
			u.ID, string(*salesType),
		); err != nil {
			return derrors.Wrap(derrors.KindInternal, "db.sales_profile", "insert sales profile", err)
		}
	}
	if techGrade != nil {
		if _, err := tx.Exec(ctx,
			`INSERT INTO identity.technician_profiles (user_id, grade) VALUES ($1, $2)`,
			u.ID, string(*techGrade),
		); err != nil {
			return derrors.Wrap(derrors.KindInternal, "db.tech_profile", "insert tech profile", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

// Update patches a user's editable fields plus the two extension tables.
// All in one transaction. Nil pointers mean "leave alone"; Clear* flags
// mean "set to NULL or remove the extension row".
func (r *UserRepository) Update(ctx context.Context, in port.UpdateUserInput) (*domain.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Build a dynamic SET clause for the columns the caller actually wants
	// to change. Positional args; keep order in sync.
	sets := []string{"updated_at = NOW()"}
	args := []any{in.ID}
	idx := 2

	add := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}

	if in.EmployeeID != nil {
		add("employee_id", strings.TrimSpace(*in.EmployeeID))
	}
	if in.FullName != nil {
		add("full_name", strings.TrimSpace(*in.FullName))
	}
	if in.Phone != nil {
		add("phone", strings.TrimSpace(*in.Phone))
	}
	if in.ClearBranch {
		sets = append(sets, "branch_id = NULL", "branch_level = NULL")
	} else if in.BranchID != nil {
		add("branch_id", *in.BranchID)
		if in.BranchLevel != nil {
			add("branch_level", string(*in.BranchLevel))
		}
	}
	if in.ClearReportsTo {
		sets = append(sets, "reports_to_user_id = NULL")
	} else if in.ReportsToID != nil {
		add("reports_to_user_id", *in.ReportsToID)
	}

	if len(sets) > 1 {
		sql := fmt.Sprintf(`UPDATE identity.users SET %s WHERE id = $1`, strings.Join(sets, ", "))
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.user_update", "update user", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, derrors.NotFound("user.not_found", "user not found")
		}
	}

	// --- Sales profile upsert / delete ---
	if in.ClearSalesType {
		if _, err := tx.Exec(ctx, `DELETE FROM identity.sales_rep_profiles WHERE user_id = $1`, in.ID); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.sales_clear", "clear sales profile", err)
		}
	} else if in.SalesType != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.sales_rep_profiles (user_id, sales_type)
			VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET sales_type = EXCLUDED.sales_type
		`, in.ID, string(*in.SalesType)); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.sales_upsert", "upsert sales profile", err)
		}
	}

	// --- Technician profile upsert / delete ---
	if in.ClearTechGrade {
		if _, err := tx.Exec(ctx, `DELETE FROM identity.technician_profiles WHERE user_id = $1`, in.ID); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.tech_clear", "clear tech profile", err)
		}
	} else if in.TechnicianGrade != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.technician_profiles (user_id, grade)
			VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET grade = EXCLUDED.grade
		`, in.ID, string(*in.TechnicianGrade)); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.tech_upsert", "upsert tech profile", err)
		}
	}

	// Re-read the user with the same transaction so we return a consistent view.
	row := tx.QueryRow(ctx, selectUserBaseSQL+" WHERE id = $1", in.ID)
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return u, nil
}

// GetSalesType returns nil if the user has no sales profile.
func (r *UserRepository) GetSalesType(ctx context.Context, id uuid.UUID) (*domain.SalesType, error) {
	var v string
	err := r.pool.QueryRow(ctx,
		`SELECT sales_type FROM identity.sales_rep_profiles WHERE user_id = $1`, id,
	).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.sales_get", "get sales profile", err)
	}
	st := domain.SalesType(v)
	return &st, nil
}

// GetTechnicianGrade returns nil if the user has no technician profile.
func (r *UserRepository) GetTechnicianGrade(ctx context.Context, id uuid.UUID) (*domain.TechnicianGrade, error) {
	var v string
	err := r.pool.QueryRow(ctx,
		`SELECT grade FROM identity.technician_profiles WHERE user_id = $1`, id,
	).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.tech_get", "get tech profile", err)
	}
	g := domain.TechnicianGrade(v)
	return &g, nil
}

const selectUserBaseSQL = `
SELECT id, employee_id, full_name, email, phone, password_hash,
       reports_to_user_id, branch_id, branch_level, active, created_at, updated_at
FROM identity.users
`

func (r *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	row := r.pool.QueryRow(ctx, selectUserBaseSQL+" WHERE id = $1", id)
	return scanUser(row)
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := r.pool.QueryRow(ctx, selectUserBaseSQL+" WHERE email = $1", email)
	return scanUser(row)
}

func (r *UserRepository) RolesForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.name
		FROM identity.user_roles ur
		JOIN identity.roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1
		ORDER BY r.name
	`, userID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_query", "query roles", err)
	}
	defer rows.Close()

	roles := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.role_scan", "scan role", err)
		}
		roles = append(roles, name)
	}
	return roles, nil
}

// PermissionsForUser returns the canonical "module.action" keys granted to
// a user via any of their roles. Distinct — duplicates across roles collapse.
func (r *UserRepository) PermissionsForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT p.module || '.' || p.action AS key
		FROM identity.user_roles ur
		JOIN identity.role_permissions rp ON rp.role_id = ur.role_id
		JOIN identity.permissions p       ON p.id      = rp.permission_id
		WHERE ur.user_id = $1
		ORDER BY key
	`, userID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.perm_query", "query permissions", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.perm_scan", "scan permission", err)
		}
		out = append(out, key)
	}
	return out, nil
}

// List paginates users with optional filters. Roles are aggregated per user
// in a single round trip via array_agg.
func (r *UserRepository) List(ctx context.Context, f port.UserListFilter) ([]port.UserListItem, int, error) {
	conditions := []string{"1=1"}
	args := []any{}
	idx := 1

	if s := strings.TrimSpace(f.Search); s != "" {
		conditions = append(conditions, fmt.Sprintf("(u.email ILIKE $%d OR u.full_name ILIKE $%d)", idx, idx))
		args = append(args, "%"+s+"%")
		idx++
	}
	if f.BranchID != nil {
		conditions = append(conditions, fmt.Sprintf("u.branch_id = $%d", idx))
		args = append(args, *f.BranchID)
		idx++
	}
	if f.Active != nil {
		conditions = append(conditions, fmt.Sprintf("u.active = $%d", idx))
		args = append(args, *f.Active)
		idx++
	}
	if f.Role != "" {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM identity.user_roles ur
			JOIN identity.roles ro ON ro.id = ur.role_id
			WHERE ur.user_id = u.id AND ro.name = $%d
		)`, idx))
		args = append(args, f.Role)
		idx++
	}
	where := strings.Join(conditions, " AND ")

	// Total.
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.users u WHERE `+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.user_count", "count users", err)
	}

	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	listSQL := `
		SELECT u.id, u.employee_id, u.full_name, u.email, u.phone, u.password_hash,
		       u.reports_to_user_id, u.branch_id, u.branch_level, u.active,
		       u.created_at, u.updated_at,
		       COALESCE(array_agg(r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles
		FROM identity.users u
		LEFT JOIN identity.user_roles ur ON ur.user_id = u.id
		LEFT JOIN identity.roles r       ON r.id = ur.role_id
		WHERE ` + where + `
		GROUP BY u.id
		ORDER BY u.full_name
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)

	args = append(args, f.Limit, f.Offset)

	rows, err := r.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.user_list", "list users", err)
	}
	defer rows.Close()

	out := []port.UserListItem{}
	for rows.Next() {
		var (
			u           domain.User
			branchLevel *string
			roles       []string
		)
		if err := rows.Scan(
			&u.ID, &u.EmployeeID, &u.FullName, &u.Email, &u.Phone, &u.PasswordHash,
			&u.ReportsToID, &u.BranchID, &branchLevel, &u.Active,
			&u.CreatedAt, &u.UpdatedAt, &roles,
		); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "db.user_scan", "scan user", err)
		}
		if branchLevel != nil {
			lvl := domain.BranchLevel(*branchLevel)
			u.BranchLevel = &lvl
		}
		out = append(out, port.UserListItem{User: u, Roles: roles})
	}
	return out, total, nil
}

// SetActive toggles a user's active flag. Inactive users cannot log in
// (enforced in the usecase Login flow).
func (r *UserRepository) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	tag, err := r.pool.Exec(ctx, `UPDATE identity.users SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.user_set_active", "set active", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("user.not_found", "user not found")
	}
	return nil
}

// CountActive returns (activeCount, totalCount). Used by the dashboard.
func (r *UserRepository) CountActive(ctx context.Context) (int, int, error) {
	var active, total int
	if err := r.pool.QueryRow(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE active = TRUE) AS active,
		  COUNT(*) AS total
		FROM identity.users
	`).Scan(&active, &total); err != nil {
		return 0, 0, derrors.Wrap(derrors.KindInternal, "db.user_count_active", "count active", err)
	}
	return active, total, nil
}

// ReplaceRolesForUser atomically swaps a user's role set. Empty roleNames
// removes all roles.
func (r *UserRepository) ReplaceRolesForUser(ctx context.Context, userID uuid.UUID, roleNames []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM identity.user_roles WHERE user_id = $1`, userID); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.role_delete", "delete user roles", err)
	}
	for _, name := range roleNames {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.user_roles (user_id, role_id, assigned_at)
			SELECT $1, id, NOW() FROM identity.roles WHERE name = $2
		`, userID, name); err != nil {
			return derrors.Wrap(derrors.KindInternal, "db.role_assign", "assign role", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

// --- internal helpers ---

func scanUser(row pgx.Row) (*domain.User, error) {
	var (
		u           domain.User
		branchLevel *string
	)
	err := row.Scan(
		&u.ID, &u.EmployeeID, &u.FullName, &u.Email, &u.Phone, &u.PasswordHash,
		&u.ReportsToID, &u.BranchID, &branchLevel, &u.Active, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("user.not_found", "user not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.scan", "scan user", err)
	}
	if branchLevel != nil {
		lvl := domain.BranchLevel(*branchLevel)
		u.BranchLevel = &lvl
	}
	return &u, nil
}

// mapInsertError translates Postgres errors to domain errors.
// Unique-violation on email becomes a Conflict; everything else is Internal.
func mapInsertError(err error) error {
	// 23505 = unique_violation. We avoid the explicit pgconn dep here by
	// matching on the SQLSTATE string from pgx's error implementation when
	// it's available; if not, we fall back to an internal error. A future
	// refactor can use pgconn.PgError for stricter matching.
	if err != nil && err.Error() != "" && containsSQLState(err.Error(), "23505") {
		return derrors.Conflict("user.email_taken", "email already in use")
	}
	return derrors.Wrap(derrors.KindInternal, "db.insert", "insert user", err)
}

func containsSQLState(msg, code string) bool {
	// crude contains check — sufficient for the unique-violation case where
	// pgx surfaces "SQLSTATE 23505" in the error message.
	for i := 0; i+len(code) <= len(msg); i++ {
		if msg[i:i+len(code)] == code {
			return true
		}
	}
	return false
}
