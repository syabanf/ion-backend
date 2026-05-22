package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type TransferRepository struct {
	pool *pgxpool.Pool
}

func NewTransferRepository(pool *pgxpool.Pool) *TransferRepository {
	return &TransferRepository{pool: pool}
}

var _ port.TransferRepository = (*TransferRepository)(nil)

func (r *TransferRepository) Create(ctx context.Context, t *domain.Transfer) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.transfers
			(id, transfer_number, source_warehouse_id, destination_warehouse_id,
			 status, notes, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
	`, t.ID, t.TransferNumber, t.SourceWarehouseID, t.DestinationWarehouseID,
		string(t.Status), nullableString(t.Notes), t.CreatedBy, t.CreatedAt); err != nil {
		return mapDBError(err, "transfer.create", "create transfer")
	}
	for _, it := range t.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.transfer_items (id, transfer_id, stock_item_id, asset_id, quantity)
			VALUES ($1, $2, $3, $4, $5)
		`, it.ID, t.ID, it.StockItemID, it.AssetID, it.Quantity); err != nil {
			return mapDBError(err, "transfer_item.create", "create transfer item")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

const transferSelect = `
SELECT id, transfer_number, source_warehouse_id, destination_warehouse_id,
       status, COALESCE(notes,''), created_by, dispatched_at, received_at,
       created_at, updated_at
FROM warehouse.transfers
`

func (r *TransferRepository) List(ctx context.Context, status string, limit, offset int) ([]domain.Transfer, int, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []any{}
	where := ""
	if status != "" {
		where = " WHERE status = $1"
		args = append(args, status)
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM warehouse.transfers"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.transfer_count", "count transfers", err)
	}
	sql := transferSelect + where + " ORDER BY created_at DESC LIMIT $" +
		intStr(len(args)+1) + " OFFSET $" + intStr(len(args)+2)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.transfer_list", "list transfers", err)
	}
	defer rows.Close()
	out := []domain.Transfer{}
	for rows.Next() {
		t, err := scanTransferHeader(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, nil
}

func (r *TransferRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Transfer, error) {
	row := r.pool.QueryRow(ctx, transferSelect+" WHERE id = $1", id)
	t, err := scanTransferHeader(row)
	if err != nil {
		return nil, err
	}
	// items
	rows, err := r.pool.Query(ctx, `
		SELECT id, transfer_id, stock_item_id, asset_id, quantity
		FROM warehouse.transfer_items
		WHERE transfer_id = $1
	`, id)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.transfer_items", "load items", err)
	}
	defer rows.Close()
	for rows.Next() {
		var it domain.TransferItem
		if err := rows.Scan(&it.ID, &it.TransferID, &it.StockItemID, &it.AssetID, &it.Quantity); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.transfer_item_scan", "scan item", err)
		}
		t.Items = append(t.Items, it)
	}
	return t, nil
}

func (r *TransferRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.TransferStatus, ts *time.Time) error {
	col := ""
	switch status {
	case domain.TransferStatusDispatched:
		col = "dispatched_at"
	case domain.TransferStatusReceived:
		col = "received_at"
	}
	var sql string
	args := []any{id, string(status)}
	if col != "" && ts != nil {
		sql = "UPDATE warehouse.transfers SET status = $2, " + col + " = $3 WHERE id = $1"
		args = append(args, *ts)
	} else {
		sql = "UPDATE warehouse.transfers SET status = $2 WHERE id = $1"
	}
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return mapDBError(err, "transfer.update_status", "update status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("transfer.not_found", "transfer not found")
	}
	return nil
}

func scanTransferHeader(row pgx.Row) (*domain.Transfer, error) {
	var (
		t      domain.Transfer
		status string
	)
	err := row.Scan(&t.ID, &t.TransferNumber, &t.SourceWarehouseID, &t.DestinationWarehouseID,
		&status, &t.Notes, &t.CreatedBy, &t.DispatchedAt, &t.ReceivedAt,
		&t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("transfer.not_found", "transfer not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.transfer_scan", "scan transfer", err)
	}
	t.Status = domain.TransferStatus(status)
	return &t, nil
}

// intStr — small inline helper to avoid pulling strconv just for $N positional args.
func intStr(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	buf := make([]byte, 0, 8)
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	if negative {
		buf = append([]byte("-"), buf...)
	}
	return string(buf)
}
