package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ApprovalTemplateRepository struct {
	pool *pgxpool.Pool
}

func NewApprovalTemplateRepository(pool *pgxpool.Pool) *ApprovalTemplateRepository {
	return &ApprovalTemplateRepository{pool: pool}
}

var _ port.ApprovalTemplateRepository = (*ApprovalTemplateRepository)(nil)

const aptCols = `id, key, name, mode, COALESCE(description,''), active, published_at, created_at, updated_at`
const aptMemberCols = `id, template_id, user_id, step_no, COALESCE(role_tag,''), created_at`

func (r *ApprovalTemplateRepository) List(ctx context.Context, activeOnly bool) ([]domain.ApprovalTemplate, error) {
	sql := `SELECT ` + aptCols + ` FROM enterprise.approval_templates`
	if activeOnly {
		sql += ` WHERE active = TRUE`
	}
	sql += ` ORDER BY key`
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.apt_list", "list approval templates", err)
	}
	defer rows.Close()
	out := []domain.ApprovalTemplate{}
	for rows.Next() {
		t, err := scanApprovalTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *ApprovalTemplateRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ApprovalTemplate, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+aptCols+` FROM enterprise.approval_templates WHERE id = $1`, id)
	t, err := scanApprovalTemplate(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *ApprovalTemplateRepository) FindByKey(ctx context.Context, key string) (*domain.ApprovalTemplate, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+aptCols+` FROM enterprise.approval_templates WHERE key = $1`, key)
	t, err := scanApprovalTemplate(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Create writes both the template AND its members in one transaction
// so a half-published template can never exist on disk.
func (r *ApprovalTemplateRepository) Create(
	ctx context.Context,
	t *domain.ApprovalTemplate,
	members []domain.ApprovalTemplateMember,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.apt_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO enterprise.approval_templates
			(id, key, name, mode, description, active, published_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, t.ID, t.Key, t.Name, string(t.Mode), t.Description, t.Active, t.PublishedAt, t.CreatedAt, t.UpdatedAt); err != nil {
		return mapDBError(err, "approval_template", "insert approval template")
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.approval_template_members
				(id, template_id, user_id, step_no, role_tag, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, m.ID, t.ID, m.UserID, m.StepNo, m.RoleTag, m.CreatedAt); err != nil {
			return mapDBError(err, "approval_template_member", "insert member")
		}
	}
	return tx.Commit(ctx)
}

func (r *ApprovalTemplateRepository) Update(ctx context.Context, t *domain.ApprovalTemplate) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.approval_templates
		SET name = $2, description = $3, active = $4, published_at = $5, updated_at = NOW()
		WHERE id = $1
	`, t.ID, t.Name, t.Description, t.Active, t.PublishedAt)
	if err != nil {
		return mapDBError(err, "approval_template", "update approval template")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("approval_template.not_found", "approval template not found")
	}
	return nil
}

func (r *ApprovalTemplateRepository) ListMembers(ctx context.Context, templateID uuid.UUID) ([]domain.ApprovalTemplateMember, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+aptMemberCols+`
		FROM enterprise.approval_template_members
		WHERE template_id = $1
		ORDER BY step_no, user_id
	`, templateID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.apt_members_list", "list template members", err)
	}
	defer rows.Close()
	out := []domain.ApprovalTemplateMember{}
	for rows.Next() {
		var m domain.ApprovalTemplateMember
		if err := rows.Scan(&m.ID, &m.TemplateID, &m.UserID, &m.StepNo, &m.RoleTag, &m.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.apt_member_scan", "scan template member", err)
		}
		out = append(out, m)
	}
	return out, nil
}

// ReplaceMembers nukes existing members + inserts the new set in one tx.
// Used by UpdateApprovalTemplate when the caller passes a new member list.
func (r *ApprovalTemplateRepository) ReplaceMembers(ctx context.Context, templateID uuid.UUID, members []domain.ApprovalTemplateMember) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.apt_replace_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM enterprise.approval_template_members WHERE template_id = $1`, templateID); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.apt_members_delete", "delete members", err)
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.approval_template_members
				(id, template_id, user_id, step_no, role_tag, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, m.ID, templateID, m.UserID, m.StepNo, m.RoleTag, m.CreatedAt); err != nil {
			return mapDBError(err, "approval_template_member", "insert member")
		}
	}
	return tx.Commit(ctx)
}

func scanApprovalTemplate(row pgx.Row) (domain.ApprovalTemplate, error) {
	var t domain.ApprovalTemplate
	var mode string
	err := row.Scan(&t.ID, &t.Key, &t.Name, &mode, &t.Description, &t.Active, &t.PublishedAt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ApprovalTemplate{}, derrors.NotFound("approval_template.not_found", "approval template not found")
	}
	if err != nil {
		return domain.ApprovalTemplate{}, derrors.Wrap(derrors.KindInternal, "db.apt_scan", "scan approval template", err)
	}
	t.Mode = domain.ApprovalMode(mode)
	return t, nil
}
