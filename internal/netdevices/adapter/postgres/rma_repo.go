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

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// RMARepository — netdev.rma_records
type RMARepository struct {
	pool *pgxpool.Pool
}

func NewRMARepository(pool *pgxpool.Pool) *RMARepository {
	return &RMARepository{pool: pool}
}

var _ port.RMARepository = (*RMARepository)(nil)

const rmaCols = `
	id, device_id,
	COALESCE(vendor, ''), COALESCE(vendor_rma_no, ''),
	COALESCE(return_reason, ''),
	shipped_at, received_at,
	COALESCE(replacement_serial, ''),
	status,
	COALESCE(notes, ''),
	created_by, created_at, updated_at
`

func (r *RMARepository) Create(ctx context.Context, rec *domain.RMARecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.rma_records
			(id, device_id, vendor, vendor_rma_no, return_reason,
			 shipped_at, received_at, replacement_serial,
			 status, notes, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`,
		rec.ID, rec.DeviceID,
		nullableString(rec.Vendor), nullableString(rec.VendorRMANo),
		nullableString(rec.ReturnReason),
		rec.ShippedAt, rec.ReceivedAt,
		nullableString(rec.ReplacementSerial),
		string(rec.Status),
		nullableString(rec.Notes),
		rec.CreatedBy, rec.CreatedAt, rec.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "rma", "insert RMA")
	}
	return nil
}

func (r *RMARepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.RMARecord, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+rmaCols+` FROM netdev.rma_records WHERE id = $1`, id)
	rec, err := scanRMA(row)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *RMARepository) UpdateLifecycle(ctx context.Context, rec *domain.RMARecord) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE netdev.rma_records SET
			vendor_rma_no = $2,
			shipped_at = $3,
			received_at = $4,
			replacement_serial = $5,
			status = $6,
			notes = $7,
			updated_at = NOW()
		WHERE id = $1
	`,
		rec.ID,
		nullableString(rec.VendorRMANo),
		rec.ShippedAt, rec.ReceivedAt,
		nullableString(rec.ReplacementSerial),
		string(rec.Status),
		nullableString(rec.Notes),
	)
	if err != nil {
		return mapDBError(err, "rma", "update RMA")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("rma.not_found", "RMA not found")
	}
	return nil
}

func (r *RMARepository) ListByStatus(ctx context.Context, status string, limit, offset int) ([]domain.RMARecord, int, error) {
	var wh []string
	var args []any
	if status != "" {
		args = append(args, status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM netdev.rma_records`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "rma.count", "count RMA records", err)
	}
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + rmaCols + ` FROM netdev.rma_records` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "rma.list", "list RMA records", err)
	}
	defer rows.Close()
	out := []domain.RMARecord{}
	for rows.Next() {
		rec, err := scanRMA(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	return out, total, nil
}

func (r *RMARepository) ListExpirable(ctx context.Context, now time.Time) ([]domain.RMARecord, error) {
	cutoff := now.Add(-90 * 24 * time.Hour)
	rows, err := r.pool.Query(ctx, `
		SELECT `+rmaCols+`
		FROM netdev.rma_records
		WHERE status NOT IN ('closed','expired')
		  AND updated_at < $1
		ORDER BY updated_at ASC
		LIMIT 500
	`, cutoff)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "rma.list_expirable", "list expirable RMA records", err)
	}
	defer rows.Close()
	out := []domain.RMARecord{}
	for rows.Next() {
		rec, err := scanRMA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func scanRMA(row pgx.Row) (domain.RMARecord, error) {
	var rec domain.RMARecord
	var status string
	err := row.Scan(
		&rec.ID, &rec.DeviceID,
		&rec.Vendor, &rec.VendorRMANo,
		&rec.ReturnReason,
		&rec.ShippedAt, &rec.ReceivedAt,
		&rec.ReplacementSerial,
		&status,
		&rec.Notes,
		&rec.CreatedBy, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RMARecord{}, derrors.NotFound("rma.not_found", "RMA not found")
	}
	if err != nil {
		return domain.RMARecord{}, derrors.Wrap(derrors.KindInternal, "rma.scan", "scan RMA", err)
	}
	rec.Status = domain.RMAStatus(status)
	return rec, nil
}
