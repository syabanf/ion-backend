package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ApprovalInstanceRepository struct {
	pool *pgxpool.Pool
}

func NewApprovalInstanceRepository(pool *pgxpool.Pool) *ApprovalInstanceRepository {
	return &ApprovalInstanceRepository{pool: pool}
}

var _ port.ApprovalInstanceRepository = (*ApprovalInstanceRepository)(nil)

const apiCols = `
	id, boq_version_id, template_id, step_no, approver_user_id,
	COALESCE(role_tag,''), status,
	COALESCE(reason_code,''), COALESCE(comment,''),
	acted_at, acted_at_original,
	COALESCE(reset_reason, ''),
	created_at, updated_at
`

func (r *ApprovalInstanceRepository) ListByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.ApprovalInstance, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+apiCols+`
		FROM enterprise.approval_instances
		WHERE boq_version_id = $1
		ORDER BY step_no, approver_user_id
	`, boqVersionID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.approval_instance_list", "list approval instances", err)
	}
	defer rows.Close()
	out := []domain.ApprovalInstance{}
	for rows.Next() {
		a, err := scanApprovalInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *ApprovalInstanceRepository) List(ctx context.Context, f port.ApprovalInstanceListFilter) ([]domain.ApprovalInstance, error) {
	var wh []string
	var args []any
	if f.PendingForUserID != nil {
		args = append(args, *f.PendingForUserID)
		wh = append(wh, fmt.Sprintf("approver_user_id = $%d AND status = 'pending'", len(args)))
	}
	if f.BOQVersionID != nil {
		args = append(args, *f.BOQVersionID)
		wh = append(wh, fmt.Sprintf("boq_version_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit, f.Offset)
	sql := `SELECT ` + apiCols + ` FROM enterprise.approval_instances` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.approval_instance_list", "list approval instances", err)
	}
	defer rows.Close()
	out := []domain.ApprovalInstance{}
	for rows.Next() {
		a, err := scanApprovalInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *ApprovalInstanceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ApprovalInstance, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+apiCols+` FROM enterprise.approval_instances WHERE id = $1`, id)
	a, err := scanApprovalInstance(row)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateBatch materializes all approval steps for a BOQ in one tx —
// matches the BOQ-submit semantics where the chain appears atomically.
func (r *ApprovalInstanceRepository) CreateBatch(ctx context.Context, instances []domain.ApprovalInstance) error {
	if len(instances) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.api_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, a := range instances {
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.approval_instances
				(id, boq_version_id, template_id, step_no, approver_user_id,
				 role_tag, status, reason_code, comment, acted_at, acted_at_original,
				 created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`,
			a.ID, a.BOQVersionID, a.TemplateID, a.StepNo, a.ApproverUserID,
			a.RoleTag, string(a.Status), string(a.ReasonCode), a.Comment,
			a.ActedAt, a.ActedAtOriginal, a.CreatedAt, a.UpdatedAt,
		); err != nil {
			return mapDBError(err, "approval_instance", "insert approval instance")
		}
	}
	return tx.Commit(ctx)
}

func (r *ApprovalInstanceRepository) Update(ctx context.Context, a *domain.ApprovalInstance) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.approval_instances
		SET status = $2, reason_code = $3, comment = $4,
		    acted_at = $5, acted_at_original = $6,
		    reset_reason = $7,
		    approver_user_id = $8,
		    updated_at = NOW()
		WHERE id = $1
	`,
		a.ID, string(a.Status), string(a.ReasonCode), a.Comment,
		a.ActedAt, a.ActedAtOriginal,
		a.ResetReason,
		a.ApproverUserID,
	)
	if err != nil {
		return mapDBError(err, "approval_instance", "update approval instance")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("approval_instance.not_found", "approval instance not found")
	}
	return nil
}

func scanApprovalInstance(row pgx.Row) (domain.ApprovalInstance, error) {
	var (
		a          domain.ApprovalInstance
		status     string
		reasonCode string
	)
	err := row.Scan(
		&a.ID, &a.BOQVersionID, &a.TemplateID, &a.StepNo, &a.ApproverUserID,
		&a.RoleTag, &status, &reasonCode, &a.Comment,
		&a.ActedAt, &a.ActedAtOriginal,
		&a.ResetReason,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ApprovalInstance{}, derrors.NotFound("approval_instance.not_found", "approval instance not found")
	}
	if err != nil {
		return domain.ApprovalInstance{}, derrors.Wrap(derrors.KindInternal, "db.api_scan", "scan approval instance", err)
	}
	a.Status = domain.ApprovalInstanceStatus(status)
	a.ReasonCode = domain.ApprovalReasonCode(reasonCode)
	return a, nil
}
