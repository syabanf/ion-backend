package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type WORepository struct {
	pool *pgxpool.Pool
}

func NewWORepository(pool *pgxpool.Pool) *WORepository {
	return &WORepository{pool: pool}
}

var _ port.WORepository = (*WORepository)(nil)

// woSelect — header projection with friendly names joined in.
//
// We pull team name + team-leader name via LEFT JOINs so the WO list
// renders without N+1 lookups. Branch is pulled the same way.
const woSelect = `
SELECT w.id, w.wo_number, w.order_id, w.customer_id, w.wo_type,
       w.product_type, COALESCE(w.maintenance_subtype,''),
       w.address, w.branch_id, w.priority, w.status,
       w.scheduled_date, w.sla_due_at, w.team_id, w.team_leader_id,
       w.is_emergency, w.is_cross_area, COALESCE(w.notes,''),
       w.created_by, w.created_at, w.updated_at,
       COALESCE(b.name,'') AS branch_name,
       COALESCE(b.code,'') AS branch_code,
       COALESCE(t.name,'') AS team_name,
       COALESCE(u.full_name,'') AS team_leader_name
FROM field.work_orders w
LEFT JOIN identity.branches b ON b.id = w.branch_id
LEFT JOIN field.teams t       ON t.id = w.team_id
LEFT JOIN identity.users u    ON u.id = w.team_leader_id
`

func (r *WORepository) Create(ctx context.Context, w *domain.WorkOrder) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO field.work_orders (
			id, wo_number, order_id, customer_id, wo_type,
			product_type, maintenance_subtype, address, branch_id,
			priority, status, scheduled_date, sla_due_at, team_id, team_leader_id,
			is_emergency, is_cross_area, notes, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20)
	`,
		w.ID, w.WONumber, w.OrderID, w.CustomerID, string(w.WOType),
		w.ProductType, nullableString(w.MaintenanceSubtype), w.Address, w.BranchID,
		string(w.Priority), string(w.Status), w.ScheduledDate, w.SLADueAt, w.TeamID, w.TeamLeaderID,
		w.IsEmergency, w.IsCrossArea, nullableString(w.Notes), w.CreatedBy, w.CreatedAt,
	)
	return mapDBError(err, "wo.create", "create work order")
}

func (r *WORepository) Update(ctx context.Context, w *domain.WorkOrder) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE field.work_orders SET
		    scheduled_date = $2,
		    sla_due_at     = $3,
		    team_id        = $4,
		    team_leader_id = $5,
		    priority       = $6,
		    status         = $7,
		    is_emergency   = $8,
		    is_cross_area  = $9,
		    notes          = $10,
		    updated_at     = NOW()
		WHERE id = $1
	`, w.ID, w.ScheduledDate, w.SLADueAt, w.TeamID, w.TeamLeaderID,
		string(w.Priority), string(w.Status),
		w.IsEmergency, w.IsCrossArea, nullableString(w.Notes))
	if err != nil {
		return mapDBError(err, "wo.update", "update work order")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("wo.not_found", "work order not found")
	}
	return nil
}

func (r *WORepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.WOStatus) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE field.work_orders SET status = $2, updated_at = NOW() WHERE id = $1`,
		id, string(status),
	)
	if err != nil {
		return mapDBError(err, "wo.update_status", "update wo status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("wo.not_found", "work order not found")
	}
	return nil
}

func (r *WORepository) List(ctx context.Context, f port.WOListFilter) ([]port.WODetail, int, error) {
	var (
		args  []any
		conds []string
	)
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "w.status = $"+itoa(len(args)))
	}
	if f.BranchID != nil {
		args = append(args, *f.BranchID)
		conds = append(conds, "w.branch_id = $"+itoa(len(args)))
	}
	if f.TeamID != nil {
		args = append(args, *f.TeamID)
		conds = append(conds, "w.team_id = $"+itoa(len(args)))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+s+"%")
		conds = append(conds, "(w.wo_number ILIKE $"+itoa(len(args))+" OR w.address ILIKE $"+itoa(len(args))+")")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM field.work_orders w"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wo_count", "count wo", err)
	}

	if f.Limit <= 0 {
		f.Limit = 50
	}
	sql := woSelect + where + " ORDER BY w.created_at DESC LIMIT $" + itoa(len(args)+1) + " OFFSET $" + itoa(len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wo_list", "list wo", err)
	}
	defer rows.Close()
	out := []port.WODetail{}
	for rows.Next() {
		d, err := scanWOHeader(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	return out, total, nil
}

// FindByID returns the WO header only. The service composes the full
// detail by also calling assignments / checklist / resolution / BAST
// repos — keeps each repo focused and avoids monster SQL.
func (r *WORepository) FindByID(ctx context.Context, id uuid.UUID) (*port.WODetail, error) {
	row := r.pool.QueryRow(ctx, woSelect+" WHERE w.id = $1", id)
	return scanWOHeader(row)
}

func scanWOHeader(row pgx.Row) (*port.WODetail, error) {
	var (
		w        domain.WorkOrder
		woType   string
		priority string
		status   string
		out      port.WODetail
	)
	err := row.Scan(
		&w.ID, &w.WONumber, &w.OrderID, &w.CustomerID, &woType,
		&w.ProductType, &w.MaintenanceSubtype,
		&w.Address, &w.BranchID, &priority, &status,
		&w.ScheduledDate, &w.SLADueAt, &w.TeamID, &w.TeamLeaderID,
		&w.IsEmergency, &w.IsCrossArea, &w.Notes,
		&w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
		&out.BranchName, &out.BranchCode,
		&out.TeamName, &out.TeamLeaderName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("wo.not_found", "work order not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wo_scan", "scan wo", err)
	}
	w.WOType = domain.WOType(woType)
	w.Priority = domain.Priority(priority)
	w.Status = domain.WOStatus(status)
	out.WO = w
	return &out, nil
}
