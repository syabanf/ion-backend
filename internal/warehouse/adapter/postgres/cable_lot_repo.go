// Wave 117 — Cable lot + cable cut repositories.
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

type CableLotRepository struct {
	pool *pgxpool.Pool
}

func NewCableLotRepository(pool *pgxpool.Pool) *CableLotRepository {
	return &CableLotRepository{pool: pool}
}

var _ port.CableLotRepository = (*CableLotRepository)(nil)

const cableLotCols = `id, item_id, COALESCE(lot_number,''),
	total_length_meters, remaining_length_meters,
	COALESCE(drum_serial,''), supplier_id, received_at, status,
	current_warehouse_id, unit_cost_per_meter, COALESCE(notes,''),
	created_at, updated_at`

func (r *CableLotRepository) Create(ctx context.Context, l *domain.CableLot) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.cable_lots
			(id, item_id, lot_number, total_length_meters, remaining_length_meters,
			 drum_serial, supplier_id, received_at, status, current_warehouse_id,
			 unit_cost_per_meter, notes, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, l.ID, l.ItemID, nullableString(l.LotNumber),
		l.TotalLengthMeters, l.RemainingLengthMeters,
		nullableString(l.DrumSerial), l.SupplierID, l.ReceivedAt, string(l.Status),
		l.CurrentWarehouseID, l.UnitCostPerMeter, nullableString(l.Notes),
		l.CreatedAt, l.UpdatedAt)
	return mapDBError(err, "cable_lot.create", "create cable lot")
}

func (r *CableLotRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.CableLot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+cableLotCols+` FROM warehouse.cable_lots WHERE id=$1`, id)
	return scanCableLot(row)
}

func (r *CableLotRepository) List(ctx context.Context, f port.CableLotListFilter) ([]domain.CableLot, int, error) {
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
	if f.LowRemainingThresholdMeters != nil {
		args = append(args, *f.LowRemainingThresholdMeters)
		wh = append(wh, fmt.Sprintf("remaining_length_meters < $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.cable_lots`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.cable_lot_count", "count cable lots", err)
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
	sql := `SELECT ` + cableLotCols + ` FROM warehouse.cable_lots` + where +
		` ORDER BY received_at LIMIT $` + fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.cable_lot_list", "list cable lots", err)
	}
	defer rows.Close()
	out := []domain.CableLot{}
	for rows.Next() {
		l, err := scanCableLot(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *l)
	}
	return out, total, nil
}

// PersistCut wraps the lot update + cut insert in a single tx so a
// partial write can't leave remaining_length out of sync with the
// audit trail.
func (r *CableLotRepository) PersistCut(ctx context.Context, lot *domain.CableLot, cut *domain.CableCut) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.cable_cut_tx", "begin tx", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE warehouse.cable_lots
		   SET remaining_length_meters=$2, status=$3, updated_at=NOW()
		 WHERE id=$1
	`, lot.ID, lot.RemainingLengthMeters, string(lot.Status)); err != nil {
		return mapDBError(err, "cable_lot.update", "update cable lot")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.cable_cuts
			(id, cable_lot_id, cut_length_meters, used_for_wo_id, cut_by, cut_at, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, cut.ID, cut.CableLotID, cut.CutLengthMeters, cut.UsedForWOID,
		cut.CutBy, cut.CutAt, nullableString(cut.Notes)); err != nil {
		return mapDBError(err, "cable_cut.insert", "insert cable cut")
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.cable_cut_commit", "commit cable cut", err)
	}
	return nil
}

func (r *CableLotRepository) UpdateStatus(ctx context.Context, l *domain.CableLot) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.cable_lots
		   SET status=$2, notes=$3, updated_at=NOW()
		 WHERE id=$1
	`, l.ID, string(l.Status), nullableString(l.Notes))
	if err != nil {
		return mapDBError(err, "cable_lot.update_status", "update cable lot status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cable_lot.not_found", "cable lot not found")
	}
	return nil
}

func scanCableLot(row pgx.Row) (*domain.CableLot, error) {
	var l domain.CableLot
	var status string
	err := row.Scan(&l.ID, &l.ItemID, &l.LotNumber,
		&l.TotalLengthMeters, &l.RemainingLengthMeters,
		&l.DrumSerial, &l.SupplierID, &l.ReceivedAt, &status,
		&l.CurrentWarehouseID, &l.UnitCostPerMeter, &l.Notes,
		&l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("cable_lot.not_found", "cable lot not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.cable_lot_scan", "scan cable lot", err)
	}
	l.Status = domain.CableLotStatus(status)
	return &l, nil
}

// =====================================================================
// Cable cuts repository
// =====================================================================

type CableCutRepository struct {
	pool *pgxpool.Pool
}

func NewCableCutRepository(pool *pgxpool.Pool) *CableCutRepository {
	return &CableCutRepository{pool: pool}
}

var _ port.CableCutRepository = (*CableCutRepository)(nil)

const cableCutCols = `id, cable_lot_id, cut_length_meters,
	used_for_wo_id, cut_by, cut_at, COALESCE(notes,'')`

func (r *CableCutRepository) ListForLot(ctx context.Context, lotID uuid.UUID, limit, offset int) ([]domain.CableCut, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.cable_cuts WHERE cable_lot_id=$1`, lotID).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.cable_cut_count", "count cuts", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+cableCutCols+` FROM warehouse.cable_cuts
		 WHERE cable_lot_id=$1 ORDER BY cut_at DESC LIMIT $2 OFFSET $3
	`, lotID, limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.cable_cut_list", "list cuts", err)
	}
	defer rows.Close()
	out := []domain.CableCut{}
	for rows.Next() {
		c, err := scanCableCut(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	return out, total, nil
}

func (r *CableCutRepository) ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.CableCut, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+cableCutCols+` FROM warehouse.cable_cuts
		 WHERE used_for_wo_id=$1 ORDER BY cut_at DESC
	`, woID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.cable_cut_list_wo", "list cuts for wo", err)
	}
	defer rows.Close()
	out := []domain.CableCut{}
	for rows.Next() {
		c, err := scanCableCut(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func scanCableCut(row pgx.Row) (*domain.CableCut, error) {
	var c domain.CableCut
	err := row.Scan(&c.ID, &c.CableLotID, &c.CutLengthMeters,
		&c.UsedForWOID, &c.CutBy, &c.CutAt, &c.Notes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("cable_cut.not_found", "cable cut not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.cable_cut_scan", "scan cable cut", err)
	}
	return &c, nil
}
