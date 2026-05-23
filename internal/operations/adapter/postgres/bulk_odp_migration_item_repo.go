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

// BulkODPMigrationItemRepository persists operations.bulk_odp_migration_items.
type BulkODPMigrationItemRepository struct {
	pool *pgxpool.Pool
}

func NewBulkODPMigrationItemRepository(pool *pgxpool.Pool) *BulkODPMigrationItemRepository {
	return &BulkODPMigrationItemRepository{pool: pool}
}

var _ port.BulkODPMigrationItemRepository = (*BulkODPMigrationItemRepository)(nil)

const bomItemCols = `
	id, bulk_job_id, customer_id, from_olt_port_id, to_olt_port_id,
	scheduled_window_start, scheduled_window_end,
	status, wo_id, COALESCE(error_msg, ''),
	processed_at, created_at
`

func (r *BulkODPMigrationItemRepository) CreateBatch(ctx context.Context, items []domain.BulkODPMigrationItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapDBError(err, "bom_item", "begin tx")
	}
	defer tx.Rollback(ctx)
	for i := range items {
		it := items[i]
		if it.Status == "" {
			it.Status = domain.BOMItemQueued
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO operations.bulk_odp_migration_items
				(id, bulk_job_id, customer_id, from_olt_port_id, to_olt_port_id,
				 scheduled_window_start, scheduled_window_end,
				 status, wo_id, error_msg, processed_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11, $12)
		`,
			it.ID, it.BulkJobID, it.CustomerID, it.FromOLTPortID, it.ToOLTPortID,
			it.ScheduledWindowStart, it.ScheduledWindowEnd,
			string(it.Status), it.WOID, it.ErrorMsg, it.ProcessedAt, it.CreatedAt,
		)
		if err != nil {
			return mapDBError(err, "bom_item", "insert odp migration item")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return mapDBError(err, "bom_item", "commit batch")
	}
	return nil
}

func (r *BulkODPMigrationItemRepository) Update(ctx context.Context, it *domain.BulkODPMigrationItem) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.bulk_odp_migration_items
		   SET status       = $2,
		       wo_id        = $3,
		       error_msg    = NULLIF($4, ''),
		       processed_at = $5,
		       from_olt_port_id = $6
		 WHERE id = $1
	`, it.ID, string(it.Status), it.WOID, it.ErrorMsg, it.ProcessedAt, it.FromOLTPortID)
	if err != nil {
		return mapDBError(err, "bom_item", "update odp migration item")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bom_item.not_found", "odp migration item not found")
	}
	return nil
}

func (r *BulkODPMigrationItemRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkODPMigrationItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+bomItemCols+`
		  FROM operations.bulk_odp_migration_items
		 WHERE id = $1
	`, id)
	it, err := scanBOMItem(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bom_item", "find odp migration item")
	}
	return it, nil
}

func (r *BulkODPMigrationItemRepository) ListByJob(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.BulkODPMigrationItem, error) {
	if limit <= 0 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bomItemCols+`
		  FROM operations.bulk_odp_migration_items
		 WHERE bulk_job_id = $1
		 ORDER BY created_at ASC
		 LIMIT $2 OFFSET $3
	`, jobID, limit, offset)
	if err != nil {
		return nil, mapDBError(err, "bom_item", "list by job")
	}
	defer rows.Close()
	out := []domain.BulkODPMigrationItem{}
	for rows.Next() {
		it, err := scanBOMItem(rows)
		if err != nil {
			return nil, mapDBError(err, "bom_item", "scan odp migration item")
		}
		out = append(out, *it)
	}
	return out, nil
}

func (r *BulkODPMigrationItemRepository) ListUnprocessedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkODPMigrationItem, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bomItemCols+`
		  FROM operations.bulk_odp_migration_items
		 WHERE bulk_job_id = $1
		   AND status IN ('queued','validating','validated','staged')
		 ORDER BY created_at ASC
		 LIMIT $2
	`, jobID, limit)
	if err != nil {
		return nil, mapDBError(err, "bom_item", "list unprocessed")
	}
	defer rows.Close()
	out := []domain.BulkODPMigrationItem{}
	for rows.Next() {
		it, err := scanBOMItem(rows)
		if err != nil {
			return nil, mapDBError(err, "bom_item", "scan odp migration item")
		}
		out = append(out, *it)
	}
	return out, nil
}

func scanBOMItem(row pgx.Row) (*domain.BulkODPMigrationItem, error) {
	var (
		it     domain.BulkODPMigrationItem
		status string
	)
	if err := row.Scan(
		&it.ID, &it.BulkJobID, &it.CustomerID, &it.FromOLTPortID, &it.ToOLTPortID,
		&it.ScheduledWindowStart, &it.ScheduledWindowEnd,
		&status, &it.WOID, &it.ErrorMsg,
		&it.ProcessedAt, &it.CreatedAt,
	); err != nil {
		return nil, err
	}
	it.Status = domain.BulkODPMigrationItemStatus(status)
	return &it, nil
}
