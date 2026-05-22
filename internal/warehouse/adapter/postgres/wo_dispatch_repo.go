package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type WODispatchRepository struct {
	pool *pgxpool.Pool
}

func NewWODispatchRepository(pool *pgxpool.Pool) *WODispatchRepository {
	return &WODispatchRepository{pool: pool}
}

var _ port.WODispatchRepository = (*WODispatchRepository)(nil)

const woDispatchHeaderSelect = `
SELECT id, wo_id, warehouse_id, dispatched_by, status,
       planned_at, staged_at, picked_up_at, returned_at, cancelled_at,
       COALESCE(cancel_reason,''), COALESCE(notes,''), revision,
       created_at, updated_at
FROM warehouse.wo_dispatch_records
`

const woDispatchItemSelect = `
SELECT id, dispatch_id, item_id, qty, returned_qty, serial_or_qr, status,
       picked_at, picked_by, COALESCE(notes,'')
FROM warehouse.wo_dispatch_items
`

func (r *WODispatchRepository) Create(ctx context.Context, d *domain.WODispatch) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.wo_dispatch_records
			(id, wo_id, warehouse_id, dispatched_by, status,
			 planned_at, notes, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
	`, d.ID, d.WOID, d.WarehouseID, d.DispatchedBy, string(d.Status),
		d.PlannedAt, d.Notes, d.Revision, d.CreatedAt); err != nil {
		return mapDBError(err, "wo_dispatch.create", "create dispatch")
	}
	for _, it := range d.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.wo_dispatch_items
				(id, dispatch_id, item_id, qty, returned_qty, status, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, it.ID, d.ID, it.ItemID, it.Qty, it.ReturnedQty, string(it.Status),
			it.Notes); err != nil {
			return mapDBError(err, "wo_dispatch_item.create", "create dispatch item")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

func (r *WODispatchRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.WODispatch, error) {
	row := r.pool.QueryRow(ctx, woDispatchHeaderSelect+" WHERE id = $1", id)
	d, err := scanWODispatchHeader(row)
	if err != nil {
		return nil, err
	}
	items, err := r.loadItems(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	d.Items = items
	return d, nil
}

func (r *WODispatchRepository) loadItems(ctx context.Context, dispatchID uuid.UUID) ([]domain.WODispatchItem, error) {
	rows, err := r.pool.Query(ctx, woDispatchItemSelect+" WHERE dispatch_id = $1 ORDER BY id", dispatchID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wo_dispatch_items", "load items", err)
	}
	defer rows.Close()
	out := []domain.WODispatchItem{}
	for rows.Next() {
		it, err := scanWODispatchItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, nil
}

func (r *WODispatchRepository) List(ctx context.Context, f port.WODispatchListFilter) ([]domain.WODispatch, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	args := []any{}
	where := ""
	add := func(clause string, val any) {
		args = append(args, val)
		if where == "" {
			where = " WHERE " + clause + "$" + intStr(len(args))
		} else {
			where += " AND " + clause + "$" + intStr(len(args))
		}
	}
	if f.WOID != nil {
		add("wo_id = ", *f.WOID)
	}
	if f.WarehouseID != nil {
		add("warehouse_id = ", *f.WarehouseID)
	}
	if f.Status != "" {
		add("status = ", f.Status)
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM warehouse.wo_dispatch_records"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wo_dispatch_count", "count dispatches", err)
	}

	sql := woDispatchHeaderSelect + where +
		" ORDER BY planned_at DESC LIMIT $" + intStr(len(args)+1) +
		" OFFSET $" + intStr(len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wo_dispatch_list", "list dispatches", err)
	}
	defer rows.Close()

	out := []domain.WODispatch{}
	ids := []uuid.UUID{}
	for rows.Next() {
		d, err := scanWODispatchHeader(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
		ids = append(ids, d.ID)
	}
	// Hydrate items in one round trip — keeps the list endpoint cheap
	// and avoids N+1 queries when the page has many dispatches.
	if len(ids) > 0 {
		itemsByDispatch, err := r.loadItemsBulk(ctx, ids)
		if err != nil {
			return nil, 0, err
		}
		for i := range out {
			out[i].Items = itemsByDispatch[out[i].ID]
		}
	}
	return out, total, nil
}

func (r *WODispatchRepository) loadItemsBulk(ctx context.Context, dispatchIDs []uuid.UUID) (map[uuid.UUID][]domain.WODispatchItem, error) {
	rows, err := r.pool.Query(ctx,
		woDispatchItemSelect+" WHERE dispatch_id = ANY($1) ORDER BY dispatch_id, id",
		dispatchIDs)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wo_dispatch_items_bulk", "bulk load items", err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]domain.WODispatchItem{}
	for rows.Next() {
		it, err := scanWODispatchItem(rows)
		if err != nil {
			return nil, err
		}
		out[it.DispatchID] = append(out[it.DispatchID], *it)
	}
	return out, nil
}

func (r *WODispatchRepository) UpdateStatus(ctx context.Context, d *domain.WODispatch) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.wo_dispatch_records
		   SET status        = $2,
		       staged_at     = $3,
		       picked_up_at  = $4,
		       returned_at   = $5,
		       cancelled_at  = $6,
		       cancel_reason = $7,
		       notes         = $8
		 WHERE id = $1
	`, d.ID, string(d.Status), d.StagedAt, d.PickedUpAt, d.ReturnedAt,
		d.CancelledAt, d.CancelReason, d.Notes)
	if err != nil {
		return mapDBError(err, "wo_dispatch.update_status", "update dispatch")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("wo_dispatch.not_found", "dispatch not found")
	}
	return nil
}

func (r *WODispatchRepository) FindItemByID(ctx context.Context, itemID uuid.UUID) (*domain.WODispatchItem, error) {
	row := r.pool.QueryRow(ctx, woDispatchItemSelect+" WHERE id = $1", itemID)
	return scanWODispatchItem(row)
}

func (r *WODispatchRepository) UpdateItem(ctx context.Context, it *domain.WODispatchItem) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.wo_dispatch_items
		   SET qty          = $2,
		       returned_qty = $3,
		       serial_or_qr = $4,
		       status       = $5,
		       picked_at    = $6,
		       picked_by    = $7,
		       notes        = $8
		 WHERE id = $1
	`, it.ID, it.Qty, it.ReturnedQty, it.SerialOrQR, string(it.Status),
		it.PickedAt, it.PickedBy, it.Notes)
	if err != nil {
		return mapDBError(err, "wo_dispatch_item.update", "update dispatch item")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("wo_dispatch_item.not_found", "dispatch item not found")
	}
	return nil
}

// scanWODispatchHeader maps one row into a domain.WODispatch.
func scanWODispatchHeader(row pgx.Row) (*domain.WODispatch, error) {
	var (
		d      domain.WODispatch
		status string
	)
	err := row.Scan(
		&d.ID, &d.WOID, &d.WarehouseID, &d.DispatchedBy, &status,
		&d.PlannedAt, &d.StagedAt, &d.PickedUpAt, &d.ReturnedAt, &d.CancelledAt,
		&d.CancelReason, &d.Notes, &d.Revision,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("wo_dispatch.not_found", "dispatch not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wo_dispatch_scan", "scan dispatch", err)
	}
	d.Status = domain.WODispatchStatus(status)
	return &d, nil
}

func scanWODispatchItem(row pgx.Row) (*domain.WODispatchItem, error) {
	var (
		it     domain.WODispatchItem
		status string
	)
	err := row.Scan(
		&it.ID, &it.DispatchID, &it.ItemID, &it.Qty, &it.ReturnedQty,
		&it.SerialOrQR, &status,
		&it.PickedAt, &it.PickedBy, &it.Notes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("wo_dispatch_item.not_found", "dispatch item not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wo_dispatch_item_scan", "scan dispatch item", err)
	}
	it.Status = domain.WODispatchItemStatus(status)
	return &it, nil
}
