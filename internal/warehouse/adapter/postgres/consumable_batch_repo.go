// Wave 117 — Consumable batch + consumption log repositories.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ConsumableBatchRepository struct {
	pool *pgxpool.Pool
}

func NewConsumableBatchRepository(pool *pgxpool.Pool) *ConsumableBatchRepository {
	return &ConsumableBatchRepository{pool: pool}
}

var _ port.ConsumableBatchRepository = (*ConsumableBatchRepository)(nil)

const consumableBatchCols = `id, item_id, batch_no, total_qty, remaining_qty,
	expiry_date, received_at, supplier_id, current_warehouse_id, unit_cost,
	status, COALESCE(notes,''), created_at, updated_at`

func (r *ConsumableBatchRepository) Create(ctx context.Context, b *domain.ConsumableBatch) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.consumable_batches
			(id, item_id, batch_no, total_qty, remaining_qty,
			 expiry_date, received_at, supplier_id, current_warehouse_id,
			 unit_cost, status, notes, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, b.ID, b.ItemID, b.BatchNo, b.TotalQty, b.RemainingQty,
		b.ExpiryDate, b.ReceivedAt, b.SupplierID, b.CurrentWarehouseID,
		b.UnitCost, string(b.Status), nullableString(b.Notes),
		b.CreatedAt, b.UpdatedAt)
	return mapDBError(err, "consumable_batch.create", "create consumable batch")
}

func (r *ConsumableBatchRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ConsumableBatch, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+consumableBatchCols+` FROM warehouse.consumable_batches WHERE id=$1`, id)
	return scanConsumableBatch(row)
}

func (r *ConsumableBatchRepository) FindOldestInStock(ctx context.Context, itemID uuid.UUID) (*domain.ConsumableBatch, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+consumableBatchCols+` FROM warehouse.consumable_batches
		 WHERE item_id=$1 AND status IN ('in_stock','allocated') AND remaining_qty > 0
		 ORDER BY received_at LIMIT 1
	`, itemID)
	return scanConsumableBatch(row)
}

func (r *ConsumableBatchRepository) List(ctx context.Context, f port.ConsumableBatchListFilter) ([]domain.ConsumableBatch, int, error) {
	var wh []string
	var args []any
	if f.ItemID != nil {
		args = append(args, *f.ItemID)
		wh = append(wh, fmt.Sprintf("item_id=$%d", len(args)))
	}
	if f.WarehouseID != nil {
		args = append(args, *f.WarehouseID)
		wh = append(wh, fmt.Sprintf("current_warehouse_id=$%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status=$%d", len(args)))
	}
	if f.ExpiringWithinDays != nil {
		args = append(args, *f.ExpiringWithinDays)
		wh = append(wh, fmt.Sprintf("expiry_date IS NOT NULL AND expiry_date <= (NOW() + ($%d || ' days')::interval)::date", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.consumable_batches`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.consumable_batch_count", "count batches", err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + consumableBatchCols + ` FROM warehouse.consumable_batches` + where +
		` ORDER BY received_at LIMIT $` + fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.consumable_batch_list", "list batches", err)
	}
	defer rows.Close()
	out := []domain.ConsumableBatch{}
	for rows.Next() {
		b, err := scanConsumableBatch(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *b)
	}
	return out, total, nil
}

// PersistConsumption updates the batch + inserts the log row in a tx.
func (r *ConsumableBatchRepository) PersistConsumption(ctx context.Context, b *domain.ConsumableBatch, log *domain.BatchConsumptionLog) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.consume_tx", "begin tx", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE warehouse.consumable_batches
		   SET remaining_qty=$2, status=$3, updated_at=NOW()
		 WHERE id=$1
	`, b.ID, b.RemainingQty, string(b.Status)); err != nil {
		return mapDBError(err, "consumable_batch.update", "update batch")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.batch_consumption_log
			(id, consumable_batch_id, wo_id, qty_consumed, consumed_by, consumed_at, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, log.ID, log.ConsumableBatchID, log.WOID, log.QtyConsumed,
		log.ConsumedBy, log.ConsumedAt, nullableString(log.Notes)); err != nil {
		return mapDBError(err, "consumption_log.insert", "insert consumption log")
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.consume_commit", "commit consume", err)
	}
	return nil
}

func (r *ConsumableBatchRepository) UpdateStatus(ctx context.Context, b *domain.ConsumableBatch) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.consumable_batches
		   SET status=$2, notes=$3, updated_at=NOW()
		 WHERE id=$1
	`, b.ID, string(b.Status), nullableString(b.Notes))
	if err != nil {
		return mapDBError(err, "consumable_batch.update_status", "update batch status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("consumable_batch.not_found", "consumable batch not found")
	}
	return nil
}

func scanConsumableBatch(row pgx.Row) (*domain.ConsumableBatch, error) {
	var b domain.ConsumableBatch
	var status string
	err := row.Scan(&b.ID, &b.ItemID, &b.BatchNo, &b.TotalQty, &b.RemainingQty,
		&b.ExpiryDate, &b.ReceivedAt, &b.SupplierID, &b.CurrentWarehouseID,
		&b.UnitCost, &status, &b.Notes, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("consumable_batch.not_found", "consumable batch not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.consumable_batch_scan", "scan batch", err)
	}
	b.Status = domain.ConsumableBatchStatus(status)
	return &b, nil
}

// =====================================================================
// Batch consumption log
// =====================================================================

type BatchConsumptionLogRepository struct {
	pool *pgxpool.Pool
}

func NewBatchConsumptionLogRepository(pool *pgxpool.Pool) *BatchConsumptionLogRepository {
	return &BatchConsumptionLogRepository{pool: pool}
}

var _ port.BatchConsumptionLogRepository = (*BatchConsumptionLogRepository)(nil)

const batchConsumptionLogCols = `id, consumable_batch_id, wo_id, qty_consumed,
	consumed_by, consumed_at, COALESCE(notes,'')`

func (r *BatchConsumptionLogRepository) ListForBatch(ctx context.Context, batchID uuid.UUID, limit, offset int) ([]domain.BatchConsumptionLog, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.batch_consumption_log WHERE consumable_batch_id=$1`,
		batchID).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.consumption_log_count", "count log", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+batchConsumptionLogCols+` FROM warehouse.batch_consumption_log
		 WHERE consumable_batch_id=$1 ORDER BY consumed_at DESC LIMIT $2 OFFSET $3
	`, batchID, limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.consumption_log_list", "list log", err)
	}
	defer rows.Close()
	out := []domain.BatchConsumptionLog{}
	for rows.Next() {
		l, err := scanBatchConsumptionLog(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *l)
	}
	return out, total, nil
}

func (r *BatchConsumptionLogRepository) ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.BatchConsumptionLog, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+batchConsumptionLogCols+` FROM warehouse.batch_consumption_log
		 WHERE wo_id=$1 ORDER BY consumed_at DESC
	`, woID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.consumption_log_wo", "list log for wo", err)
	}
	defer rows.Close()
	out := []domain.BatchConsumptionLog{}
	for rows.Next() {
		l, err := scanBatchConsumptionLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, nil
}

func scanBatchConsumptionLog(row pgx.Row) (*domain.BatchConsumptionLog, error) {
	var l domain.BatchConsumptionLog
	err := row.Scan(&l.ID, &l.ConsumableBatchID, &l.WOID, &l.QtyConsumed,
		&l.ConsumedBy, &l.ConsumedAt, &l.Notes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("consumption_log.not_found", "consumption log not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.consumption_log_scan", "scan log", err)
	}
	return &l, nil
}
