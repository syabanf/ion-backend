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

// BulkWOCreationItemRepository persists operations.bulk_wo_creation_items.
type BulkWOCreationItemRepository struct {
	pool *pgxpool.Pool
}

func NewBulkWOCreationItemRepository(pool *pgxpool.Pool) *BulkWOCreationItemRepository {
	return &BulkWOCreationItemRepository{pool: pool}
}

var _ port.BulkWOCreationItemRepository = (*BulkWOCreationItemRepository)(nil)

const bwoItemCols = `
	id, bulk_job_id, customer_id, wo_template_id, COALESCE(wo_type, ''),
	scheduled_at, status, created_wo_id, COALESCE(error_msg, ''),
	processed_at, created_at
`

func (r *BulkWOCreationItemRepository) CreateBatch(ctx context.Context, items []domain.BulkWOCreationItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapDBError(err, "bwo_item", "begin tx")
	}
	defer tx.Rollback(ctx)
	for i := range items {
		it := items[i]
		if it.Status == "" {
			it.Status = domain.BWOItemQueued
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO operations.bulk_wo_creation_items
				(id, bulk_job_id, customer_id, wo_template_id, wo_type,
				 scheduled_at, status, created_wo_id, error_msg, processed_at, created_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''), $10, $11)
		`,
			it.ID, it.BulkJobID, it.CustomerID, it.WOTemplateID, it.WOType,
			it.ScheduledAt, string(it.Status), it.CreatedWOID, it.ErrorMsg, it.ProcessedAt, it.CreatedAt,
		)
		if err != nil {
			return mapDBError(err, "bwo_item", "insert wo creation item")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDBError(err, "bwo_item", "commit batch")
	}
	return nil
}

func (r *BulkWOCreationItemRepository) Update(ctx context.Context, it *domain.BulkWOCreationItem) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.bulk_wo_creation_items
		   SET status        = $2,
		       created_wo_id = $3,
		       error_msg     = NULLIF($4, ''),
		       processed_at  = $5
		 WHERE id = $1
	`, it.ID, string(it.Status), it.CreatedWOID, it.ErrorMsg, it.ProcessedAt)
	if err != nil {
		return mapDBError(err, "bwo_item", "update wo creation item")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bwo_item.not_found", "wo creation item not found")
	}
	return nil
}

func (r *BulkWOCreationItemRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkWOCreationItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+bwoItemCols+`
		  FROM operations.bulk_wo_creation_items
		 WHERE id = $1
	`, id)
	it, err := scanBWOItem(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bwo_item", "find wo creation item")
	}
	return it, nil
}

func (r *BulkWOCreationItemRepository) ListByJob(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.BulkWOCreationItem, error) {
	if limit <= 0 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bwoItemCols+`
		  FROM operations.bulk_wo_creation_items
		 WHERE bulk_job_id = $1
		 ORDER BY created_at ASC
		 LIMIT $2 OFFSET $3
	`, jobID, limit, offset)
	if err != nil {
		return nil, mapDBError(err, "bwo_item", "list by job")
	}
	defer rows.Close()
	out := []domain.BulkWOCreationItem{}
	for rows.Next() {
		it, err := scanBWOItem(rows)
		if err != nil {
			return nil, mapDBError(err, "bwo_item", "scan wo creation item")
		}
		out = append(out, *it)
	}
	return out, nil
}

func (r *BulkWOCreationItemRepository) ListUnprocessedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkWOCreationItem, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bwoItemCols+`
		  FROM operations.bulk_wo_creation_items
		 WHERE bulk_job_id = $1
		   AND status IN ('queued','validating','validated')
		 ORDER BY created_at ASC
		 LIMIT $2
	`, jobID, limit)
	if err != nil {
		return nil, mapDBError(err, "bwo_item", "list unprocessed")
	}
	defer rows.Close()
	out := []domain.BulkWOCreationItem{}
	for rows.Next() {
		it, err := scanBWOItem(rows)
		if err != nil {
			return nil, mapDBError(err, "bwo_item", "scan wo creation item")
		}
		out = append(out, *it)
	}
	return out, nil
}

func scanBWOItem(row pgx.Row) (*domain.BulkWOCreationItem, error) {
	var (
		it     domain.BulkWOCreationItem
		status string
	)
	if err := row.Scan(
		&it.ID, &it.BulkJobID, &it.CustomerID, &it.WOTemplateID, &it.WOType,
		&it.ScheduledAt, &status, &it.CreatedWOID, &it.ErrorMsg,
		&it.ProcessedAt, &it.CreatedAt,
	); err != nil {
		return nil, err
	}
	it.Status = domain.BulkWOCreationItemStatus(status)
	return &it, nil
}
