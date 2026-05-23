package postgres

import (
	"context"
	stderrors "errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkPlanChangeItemRepository persists operations.bulk_plan_change_items.
type BulkPlanChangeItemRepository struct {
	pool *pgxpool.Pool
}

func NewBulkPlanChangeItemRepository(pool *pgxpool.Pool) *BulkPlanChangeItemRepository {
	return &BulkPlanChangeItemRepository{pool: pool}
}

var _ port.BulkPlanChangeItemRepository = (*BulkPlanChangeItemRepository)(nil)

const bpcItemCols = `
	id, bulk_job_id, customer_id, current_plan_id, target_plan_id,
	effective_at, status, COALESCE(error_msg, ''),
	processed_at, created_at
`

func (r *BulkPlanChangeItemRepository) CreateBatch(ctx context.Context, items []domain.BulkPlanChangeItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapDBError(err, "bpc_item", "begin tx")
	}
	defer tx.Rollback(ctx)
	for i := range items {
		it := items[i]
		if it.Status == "" {
			it.Status = domain.BPCItemQueued
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO operations.bulk_plan_change_items
				(id, bulk_job_id, customer_id, current_plan_id, target_plan_id,
				 effective_at, status, error_msg, processed_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10)
		`,
			it.ID, it.BulkJobID, it.CustomerID, it.CurrentPlanID, it.TargetPlanID,
			it.EffectiveAt, string(it.Status), it.ErrorMsg, it.ProcessedAt, it.CreatedAt,
		)
		if err != nil {
			return mapDBError(err, "bpc_item", "insert plan change item")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDBError(err, "bpc_item", "commit batch")
	}
	return nil
}

func (r *BulkPlanChangeItemRepository) Update(ctx context.Context, it *domain.BulkPlanChangeItem) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.bulk_plan_change_items
		   SET status        = $2,
		       error_msg     = NULLIF($3, ''),
		       processed_at  = $4,
		       current_plan_id = $5,
		       effective_at  = $6
		 WHERE id = $1
	`, it.ID, string(it.Status), it.ErrorMsg, it.ProcessedAt, it.CurrentPlanID, it.EffectiveAt)
	if err != nil {
		return mapDBError(err, "bpc_item", "update plan change item")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bpc_item.not_found", "plan change item not found")
	}
	return nil
}

func (r *BulkPlanChangeItemRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkPlanChangeItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+bpcItemCols+`
		  FROM operations.bulk_plan_change_items
		 WHERE id = $1
	`, id)
	it, err := scanBPCItem(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bpc_item", "find plan change item")
	}
	return it, nil
}

func (r *BulkPlanChangeItemRepository) ListByJob(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.BulkPlanChangeItem, error) {
	if limit <= 0 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bpcItemCols+`
		  FROM operations.bulk_plan_change_items
		 WHERE bulk_job_id = $1
		 ORDER BY created_at ASC
		 LIMIT $2 OFFSET $3
	`, jobID, limit, offset)
	if err != nil {
		return nil, mapDBError(err, "bpc_item", "list by job")
	}
	defer rows.Close()
	out := []domain.BulkPlanChangeItem{}
	for rows.Next() {
		it, err := scanBPCItem(rows)
		if err != nil {
			return nil, mapDBError(err, "bpc_item", "scan plan change item")
		}
		out = append(out, *it)
	}
	return out, nil
}

func (r *BulkPlanChangeItemRepository) ListUnprocessedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkPlanChangeItem, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bpcItemCols+`
		  FROM operations.bulk_plan_change_items
		 WHERE bulk_job_id = $1
		   AND status IN ('queued','validating','validated','processing')
		 ORDER BY created_at ASC
		 LIMIT $2
	`, jobID, limit)
	if err != nil {
		return nil, mapDBError(err, "bpc_item", "list unprocessed")
	}
	defer rows.Close()
	out := []domain.BulkPlanChangeItem{}
	for rows.Next() {
		it, err := scanBPCItem(rows)
		if err != nil {
			return nil, mapDBError(err, "bpc_item", "scan plan change item")
		}
		out = append(out, *it)
	}
	return out, nil
}

func scanBPCItem(row pgx.Row) (*domain.BulkPlanChangeItem, error) {
	var (
		it     domain.BulkPlanChangeItem
		status string
	)
	if err := row.Scan(
		&it.ID, &it.BulkJobID, &it.CustomerID, &it.CurrentPlanID, &it.TargetPlanID,
		&it.EffectiveAt, &status, &it.ErrorMsg,
		&it.ProcessedAt, &it.CreatedAt,
	); err != nil {
		return nil, err
	}
	it.Status = domain.BulkPlanChangeItemStatus(status)
	return &it, nil
}
