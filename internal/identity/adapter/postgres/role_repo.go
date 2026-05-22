package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type RoleRepository struct {
	pool *pgxpool.Pool
}

func NewRoleRepository(pool *pgxpool.Pool) *RoleRepository {
	return &RoleRepository{pool: pool}
}

var _ port.RoleRepository = (*RoleRepository)(nil)

func (r *RoleRepository) List(ctx context.Context) ([]domain.Role, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, COALESCE(description, ''), created_at
		FROM identity.roles
		ORDER BY name
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_list", "list roles", err)
	}
	defer rows.Close()

	out := []domain.Role{}
	for rows.Next() {
		var r domain.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.role_scan", "scan role", err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (r *RoleRepository) ListPermissions(ctx context.Context) ([]domain.Permission, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, module, action, COALESCE(description, '')
		FROM identity.permissions
		ORDER BY module, action
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.perm_list", "list permissions", err)
	}
	defer rows.Close()

	out := []domain.Permission{}
	for rows.Next() {
		var p domain.Permission
		if err := rows.Scan(&p.ID, &p.Module, &p.Action, &p.Description); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.perm_scan", "scan permission", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *RoleRepository) PermissionsForRole(ctx context.Context, roleID uuid.UUID) ([]domain.Permission, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.module, p.action, COALESCE(p.description, '')
		FROM identity.role_permissions rp
		JOIN identity.permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1
		ORDER BY p.module, p.action
	`, roleID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_perm_list", "list role permissions", err)
	}
	defer rows.Close()

	out := []domain.Permission{}
	for rows.Next() {
		var p domain.Permission
		if err := rows.Scan(&p.ID, &p.Module, &p.Action, &p.Description); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.role_perm_scan", "scan role permission", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *RoleRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error) {
	var role domain.Role
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(description, ''), created_at
		FROM identity.roles WHERE id = $1
	`, id).Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("role.not_found", "role not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_find", "find role", err)
	}
	return &role, nil
}

func (r *RoleRepository) FindByName(ctx context.Context, name string) (*domain.Role, error) {
	var role domain.Role
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(description, ''), created_at
		FROM identity.roles WHERE name = $1
	`, name).Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("role.not_found", "role not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_find_by_name", "find role by name", err)
	}
	return &role, nil
}

func (r *RoleRepository) Create(ctx context.Context, name, description string) (*domain.Role, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, derrors.Validation("role.name_required", "role name is required")
	}
	var role domain.Role
	err := r.pool.QueryRow(ctx, `
		INSERT INTO identity.roles (name, description)
		VALUES ($1, $2)
		RETURNING id, name, COALESCE(description, ''), created_at
	`, name, description).Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, derrors.Conflict(
				"role.name_taken",
				"another role already uses this name",
			)
		}
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_create", "create role", err)
	}
	return &role, nil
}

func (r *RoleRepository) Update(ctx context.Context, id uuid.UUID, name, description string) (*domain.Role, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, derrors.Validation("role.name_required", "role name is required")
	}
	var role domain.Role
	err := r.pool.QueryRow(ctx, `
		UPDATE identity.roles
		SET name = $2, description = $3
		WHERE id = $1
		RETURNING id, name, COALESCE(description, ''), created_at
	`, id, name, description).Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("role.not_found", "role not found")
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, derrors.Conflict(
				"role.name_taken",
				"another role already uses this name",
			)
		}
		return nil, derrors.Wrap(derrors.KindInternal, "db.role_update", "update role", err)
	}
	return &role, nil
}

func (r *RoleRepository) Delete(ctx context.Context, id uuid.UUID) error {
	// Guard against deleting roles that are still assigned to users —
	// otherwise the operator silently loses access for those users.
	// They have to reassign first.
	var assignedCount int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.user_roles WHERE role_id = $1`, id,
	).Scan(&assignedCount); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.role_delete_count", "count assignments", err)
	}
	if assignedCount > 0 {
		return derrors.Conflict(
			"role.in_use",
			"this role is still assigned to users — reassign them before deleting",
		)
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM identity.roles WHERE id = $1`, id)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.role_delete", "delete role", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("role.not_found", "role not found")
	}
	return nil
}

func (r *RoleRepository) ReplacePermissions(ctx context.Context, roleID uuid.UUID, permissionIDs []uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.role_perm_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM identity.role_permissions WHERE role_id = $1`, roleID,
	); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.role_perm_clear", "clear role permissions", err)
	}
	for _, pid := range permissionIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.role_permissions (role_id, permission_id)
			VALUES ($1, $2)
		`, roleID, pid); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return derrors.Validation(
					"role.permission_invalid",
					"one or more permission ids do not exist",
				)
			}
			return derrors.Wrap(derrors.KindInternal, "db.role_perm_grant", "grant permission", err)
		}
	}
	return tx.Commit(ctx)
}
