package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// DeviceSwapRepository — netdev.device_swaps
type DeviceSwapRepository struct {
	pool *pgxpool.Pool
}

func NewDeviceSwapRepository(pool *pgxpool.Pool) *DeviceSwapRepository {
	return &DeviceSwapRepository{pool: pool}
}

var _ port.DeviceSwapRepository = (*DeviceSwapRepository)(nil)

const swapCols = `
	id, customer_id, faulty_device_id, replacement_device_id,
	COALESCE(reason, ''),
	fault_event_id, status, wo_id, technician_user_id,
	swap_started_at, swap_completed_at, retrofit_id,
	requested_by, approved_by,
	created_at, updated_at
`

func (r *DeviceSwapRepository) Create(ctx context.Context, s *domain.DeviceSwap) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.device_swaps
			(id, customer_id, faulty_device_id, replacement_device_id,
			 reason, fault_event_id, status, wo_id, technician_user_id,
			 swap_started_at, swap_completed_at, retrofit_id,
			 requested_by, approved_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	`,
		s.ID, s.CustomerID, s.FaultyDeviceID, s.ReplacementDeviceID,
		nullableString(s.Reason), s.FaultEventID, string(s.Status),
		s.WOID, s.TechnicianUserID,
		s.SwapStartedAt, s.SwapCompletedAt, s.RetrofitID,
		s.RequestedBy, s.ApprovedBy, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "device_swap", "insert device swap")
	}
	return nil
}

func (r *DeviceSwapRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.DeviceSwap, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+swapCols+` FROM netdev.device_swaps WHERE id = $1`, id)
	s, err := scanSwap(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *DeviceSwapRepository) UpdateLifecycle(ctx context.Context, s *domain.DeviceSwap) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE netdev.device_swaps SET
			replacement_device_id = $2,
			reason = $3,
			status = $4,
			wo_id = $5,
			technician_user_id = $6,
			swap_started_at = $7,
			swap_completed_at = $8,
			retrofit_id = $9,
			approved_by = $10,
			updated_at = NOW()
		WHERE id = $1
	`,
		s.ID,
		s.ReplacementDeviceID, nullableString(s.Reason), string(s.Status),
		s.WOID, s.TechnicianUserID,
		s.SwapStartedAt, s.SwapCompletedAt, s.RetrofitID,
		s.ApprovedBy,
	)
	if err != nil {
		return mapDBError(err, "device_swap", "update device swap")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("device_swap.not_found", "device swap not found")
	}
	return nil
}

func (r *DeviceSwapRepository) List(ctx context.Context, status string, customerID *uuid.UUID, limit, offset int) ([]domain.DeviceSwap, int, error) {
	var wh []string
	var args []any
	if status != "" {
		args = append(args, status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if customerID != nil {
		args = append(args, *customerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM netdev.device_swaps`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "device_swap.count", "count swaps", err)
	}
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + swapCols + ` FROM netdev.device_swaps` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "device_swap.list", "list swaps", err)
	}
	defer rows.Close()
	out := []domain.DeviceSwap{}
	for rows.Next() {
		s, err := scanSwap(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func scanSwap(row pgx.Row) (domain.DeviceSwap, error) {
	var s domain.DeviceSwap
	var status string
	err := row.Scan(
		&s.ID, &s.CustomerID, &s.FaultyDeviceID, &s.ReplacementDeviceID,
		&s.Reason,
		&s.FaultEventID, &status, &s.WOID, &s.TechnicianUserID,
		&s.SwapStartedAt, &s.SwapCompletedAt, &s.RetrofitID,
		&s.RequestedBy, &s.ApprovedBy,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeviceSwap{}, derrors.NotFound("device_swap.not_found", "device swap not found")
	}
	if err != nil {
		return domain.DeviceSwap{}, derrors.Wrap(derrors.KindInternal, "device_swap.scan", "scan device swap", err)
	}
	s.Status = domain.SwapStatus(status)
	return s, nil
}
