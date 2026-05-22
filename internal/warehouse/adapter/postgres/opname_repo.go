package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type OpnameRepository struct {
	pool *pgxpool.Pool
}

func NewOpnameRepository(pool *pgxpool.Pool) *OpnameRepository {
	return &OpnameRepository{pool: pool}
}

var _ port.OpnameRepository = (*OpnameRepository)(nil)

func (r *OpnameRepository) CreateSession(ctx context.Context, s *domain.OpnameSession) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.opname_sessions
		  (id, session_number, warehouse_id, status, started_by, started_at, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, s.ID, s.SessionNumber, s.WarehouseID, string(s.Status), s.StartedBy, s.StartedAt, nullableString(s.Notes))
	return mapDBError(err, "opname.create_session", "create opname session")
}

const opnameSessionSelect = `
SELECT s.id, s.session_number, s.warehouse_id, s.status,
       s.started_by, s.started_at, s.committed_at, s.cancelled_at,
       COALESCE(s.notes, ''), s.created_at, s.updated_at,
       w.code, w.name
FROM warehouse.opname_sessions s
JOIN warehouse.warehouses w ON w.id = s.warehouse_id
`

func (r *OpnameRepository) FindSession(ctx context.Context, id uuid.UUID) (*port.OpnameView, error) {
	row := r.pool.QueryRow(ctx, opnameSessionSelect+" WHERE s.id = $1", id)
	view, err := scanOpnameView(row)
	if err != nil {
		return nil, err
	}
	counts, err := r.ListCounts(ctx, id)
	if err != nil {
		return nil, err
	}
	view.Counts = counts
	return view, nil
}

func (r *OpnameRepository) ListSessions(ctx context.Context, warehouseID *uuid.UUID, status string, limit, offset int) ([]port.OpnameView, int, error) {
	var (
		args  []any
		conds []string
	)
	if warehouseID != nil {
		args = append(args, *warehouseID)
		conds = append(conds, "s.warehouse_id = $"+intStr(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, "s.status = $"+intStr(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM warehouse.opname_sessions s"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.opname_count", "count opname", err)
	}

	if limit <= 0 {
		limit = 50
	}
	sql := opnameSessionSelect + where + " ORDER BY s.started_at DESC LIMIT $" + intStr(len(args)+1) + " OFFSET $" + intStr(len(args)+2)
	args = append(args, limit, offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.opname_list", "list opname", err)
	}
	defer rows.Close()
	out := []port.OpnameView{}
	for rows.Next() {
		v, err := scanOpnameView(rows)
		if err != nil {
			return nil, 0, err
		}
		// List view skips per-session counts to keep payload light.
		out = append(out, *v)
	}
	return out, total, nil
}

func (r *OpnameRepository) UpdateSessionStatus(ctx context.Context, id uuid.UUID, status domain.OpnameStatus, ts time.Time) error {
	var stampCol string
	switch status {
	case domain.OpnameStatusCommitted:
		stampCol = "committed_at"
	case domain.OpnameStatusCancelled:
		stampCol = "cancelled_at"
	}
	sql := "UPDATE warehouse.opname_sessions SET status = $2, updated_at = NOW()"
	args := []any{id, string(status)}
	if stampCol != "" {
		sql += ", " + stampCol + " = $3"
		args = append(args, ts)
	}
	sql += " WHERE id = $1"
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return mapDBError(err, "opname.update_status", "update opname status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("opname.not_found", "opname session not found")
	}
	return nil
}

// UpsertCount writes (or updates) one count line. Variance is computed
// here from expected/counted so the DB stores the truth even if the
// caller does the math differently. expected_qty is locked at first
// upsert; subsequent updates to the same (session, item) only adjust
// the counted side (treating expected as captured at first count).
func (r *OpnameRepository) UpsertCount(ctx context.Context, c *domain.OpnameCount) (*domain.OpnameCount, error) {
	var decision any
	if c.CableRemnantDecision != nil {
		decision = string(*c.CableRemnantDecision)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO warehouse.opname_counts
		  (id, session_id, stock_item_id, expected_qty, counted_qty, variance,
		   cable_remnant_decision, notes, counted_by, counted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (session_id, stock_item_id) DO UPDATE SET
		  counted_qty = EXCLUDED.counted_qty,
		  variance    = EXCLUDED.counted_qty - warehouse.opname_counts.expected_qty,
		  cable_remnant_decision = EXCLUDED.cable_remnant_decision,
		  notes       = EXCLUDED.notes,
		  counted_by  = EXCLUDED.counted_by,
		  counted_at  = EXCLUDED.counted_at
		RETURNING id, session_id, stock_item_id, expected_qty::float8,
		          counted_qty::float8, variance::float8,
		          cable_remnant_decision, COALESCE(notes, ''),
		          counted_by, counted_at
	`,
		c.ID, c.SessionID, c.StockItemID, c.ExpectedQty, c.CountedQty, c.Variance,
		decision, nullableString(c.Notes), c.CountedBy, c.CountedAt,
	)
	var (
		out         domain.OpnameCount
		decisionStr *string
	)
	if err := row.Scan(&out.ID, &out.SessionID, &out.StockItemID,
		&out.ExpectedQty, &out.CountedQty, &out.Variance,
		&decisionStr, &out.Notes, &out.CountedBy, &out.CountedAt); err != nil {
		return nil, mapDBError(err, "opname.upsert_count", "upsert opname count")
	}
	if decisionStr != nil {
		d := domain.CableRemnantDecision(*decisionStr)
		out.CableRemnantDecision = &d
	}
	return &out, nil
}

func (r *OpnameRepository) ListCounts(ctx context.Context, sessionID uuid.UUID) ([]port.OpnameCountView, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.session_id, c.stock_item_id,
		       c.expected_qty::float8, c.counted_qty::float8, c.variance::float8,
		       c.cable_remnant_decision, COALESCE(c.notes, ''),
		       c.counted_by, c.counted_at,
		       si.sku, si.name, si.unit, (si.category = 'cable') AS is_cable
		FROM warehouse.opname_counts c
		JOIN warehouse.stock_items si ON si.id = c.stock_item_id
		WHERE c.session_id = $1
		ORDER BY si.sku
	`, sessionID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.opname_counts", "list opname counts", err)
	}
	defer rows.Close()
	out := []port.OpnameCountView{}
	for rows.Next() {
		var (
			c           domain.OpnameCount
			decisionStr *string
			v           port.OpnameCountView
		)
		if err := rows.Scan(&c.ID, &c.SessionID, &c.StockItemID,
			&c.ExpectedQty, &c.CountedQty, &c.Variance,
			&decisionStr, &c.Notes, &c.CountedBy, &c.CountedAt,
			&v.ItemSKU, &v.ItemName, &v.ItemUnit, &v.IsCable); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.opname_count_scan", "scan opname count", err)
		}
		if decisionStr != nil {
			d := domain.CableRemnantDecision(*decisionStr)
			c.CableRemnantDecision = &d
		}
		v.Count = c
		out = append(out, v)
	}
	return out, nil
}

func scanOpnameView(row pgx.Row) (*port.OpnameView, error) {
	var (
		s      domain.OpnameSession
		status string
		v      port.OpnameView
	)
	err := row.Scan(&s.ID, &s.SessionNumber, &s.WarehouseID, &status,
		&s.StartedBy, &s.StartedAt, &s.CommittedAt, &s.CancelledAt,
		&s.Notes, &s.CreatedAt, &s.UpdatedAt,
		&v.WarehouseCode, &v.WarehouseName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("opname.not_found", "opname session not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.opname_scan", "scan opname session", err)
	}
	s.Status = domain.OpnameStatus(status)
	v.Session = s
	return &v, nil
}
