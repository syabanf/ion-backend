package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// FiberLinkRepository implements port.FiberLinkRepository against
// `nocmon.fiber_links` (+ append to fiber_attenuation_history).
type FiberLinkRepository struct {
	pool *pgxpool.Pool
}

func NewFiberLinkRepository(pool *pgxpool.Pool) *FiberLinkRepository {
	return &FiberLinkRepository{pool: pool}
}

var _ port.FiberLinkRepository = (*FiberLinkRepository)(nil)

const fiberCols = `
	id, olt_port_id, COALESCE(onu_serial, ''),
	expected_loss_db, warn_threshold_db, critical_threshold_db,
	last_measured_db, last_measured_at, status, customer_id,
	created_at, updated_at
`

func (r *FiberLinkRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.FiberLink, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+fiberCols+` FROM nocmon.fiber_links WHERE id = $1`, id)
	l, err := scanFiberLink(row)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *FiberLinkRepository) List(ctx context.Context, f port.FiberListFilter) ([]domain.FiberLink, int, error) {
	args := []any{}
	wh := []string{}
	if f.Status != "" {
		args = append(args, string(f.Status))
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM nocmon.fiber_links`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.fiber_count", "count fiber links", err)
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
	sql := `SELECT ` + fiberCols + ` FROM nocmon.fiber_links` + where +
		` ORDER BY last_measured_at DESC NULLS LAST LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.fiber_list", "list fiber links", err)
	}
	defer rows.Close()
	out := []domain.FiberLink{}
	for rows.Next() {
		l, err := scanFiberLink(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, nil
}

func (r *FiberLinkRepository) ListStale(ctx context.Context, olderThan time.Time, limit int) ([]domain.FiberLink, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+fiberCols+`
		FROM nocmon.fiber_links
		WHERE (last_measured_at IS NULL OR last_measured_at < $1)
		  AND status <> 'offline'
		ORDER BY last_measured_at ASC NULLS FIRST
		LIMIT $2
	`, olderThan, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.fiber_stale", "list stale fiber links", err)
	}
	defer rows.Close()
	out := []domain.FiberLink{}
	for rows.Next() {
		l, err := scanFiberLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// UpdateMeasurement atomically:
//  1. Inserts an append-only row on fiber_attenuation_history.
//  2. Stamps last_measured_db / last_measured_at / status on
//     fiber_links.
//
// Both writes happen in the same tx so a partial update can't
// surface (the trend chart and the dashboard chip stay consistent).
func (r *FiberLinkRepository) UpdateMeasurement(
	ctx context.Context,
	linkID uuid.UUID,
	valueDB float64,
	at time.Time,
	status domain.FiberStatus,
	source string,
) (*domain.FiberLink, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.fiber_tx", "begin tx", err)
	}
	defer tx.Rollback(ctx)

	if source == "" {
		source = "snmp_poll"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO nocmon.fiber_attenuation_history
			(fiber_link_id, measured_at, value_db, source)
		VALUES ($1, $2, $3, $4)
	`, linkID, at, valueDB, source); err != nil {
		return nil, mapDBError(err, "fiber.history", "insert attenuation history")
	}

	tag, err := tx.Exec(ctx, `
		UPDATE nocmon.fiber_links
		SET last_measured_db = $2,
		    last_measured_at = $3,
		    status = $4,
		    updated_at = $3
		WHERE id = $1
	`, linkID, valueDB, at, string(status))
	if err != nil {
		return nil, mapDBError(err, "fiber", "update fiber link")
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.NotFound("fiber.not_found", "fiber link not found")
	}

	row := tx.QueryRow(ctx, `SELECT `+fiberCols+` FROM nocmon.fiber_links WHERE id = $1`, linkID)
	l, err := scanFiberLink(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.fiber_commit", "commit tx", err)
	}
	return &l, nil
}

func (r *FiberLinkRepository) MarkOffline(ctx context.Context, linkID uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE nocmon.fiber_links
		SET status = 'offline', updated_at = $2
		WHERE id = $1
	`, linkID, at)
	if err != nil {
		return mapDBError(err, "fiber", "mark offline")
	}
	return nil
}

func (r *FiberLinkRepository) ListDegraded(ctx context.Context, limit int) ([]domain.FiberLink, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+fiberCols+`
		FROM nocmon.fiber_links
		WHERE status IN ('warn','critical','offline')
		ORDER BY status DESC, last_measured_at DESC NULLS LAST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.fiber_degraded", "list degraded fiber", err)
	}
	defer rows.Close()
	out := []domain.FiberLink{}
	for rows.Next() {
		l, err := scanFiberLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func scanFiberLink(row pgx.Row) (domain.FiberLink, error) {
	var l domain.FiberLink
	var status string
	err := row.Scan(
		&l.ID, &l.OLTPortID, &l.ONUSerial,
		&l.ExpectedLossDB, &l.WarnThresholdDB, &l.CriticalThresholdDB,
		&l.LastMeasuredDB, &l.LastMeasuredAt, &status, &l.CustomerID,
		&l.CreatedAt, &l.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FiberLink{}, derrors.NotFound("fiber.not_found", "fiber link not found")
	}
	if err != nil {
		return domain.FiberLink{}, derrors.Wrap(derrors.KindInternal, "db.fiber_scan", "scan fiber link", err)
	}
	l.Status = domain.FiberStatus(status)
	return l, nil
}
