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

// DeviceRepository implements port.DeviceRepository against
// `netdev.devices`.
type DeviceRepository struct {
	pool *pgxpool.Pool
}

func NewDeviceRepository(pool *pgxpool.Pool) *DeviceRepository {
	return &DeviceRepository{pool: pool}
}

var _ port.DeviceRepository = (*DeviceRepository)(nil)

const deviceCols = `
	id, serial_no,
	COALESCE(mac_addr, ''), COALESCE(asset_tag, ''),
	kind,
	COALESCE(model, ''), COALESCE(manufacturer, ''),
	COALESCE(firmware_version, ''),
	status,
	warehouse_id, customer_id, service_location_id,
	COALESCE(ip_address, ''), COALESCE(mgmt_uri, ''),
	last_seen_at, commissioned_at, decommissioned_at,
	COALESCE(notes, ''),
	created_at, updated_at
`

func (r *DeviceRepository) Create(ctx context.Context, d *domain.Device) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.devices
			(id, serial_no, mac_addr, asset_tag, kind, model, manufacturer,
			 firmware_version, status, warehouse_id, customer_id, service_location_id,
			 ip_address, mgmt_uri, last_seen_at, commissioned_at, decommissioned_at,
			 notes, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`,
		d.ID, d.SerialNo, nullableString(d.MACAddr), nullableString(d.AssetTag),
		string(d.Kind), nullableString(d.Model), nullableString(d.Manufacturer),
		nullableString(d.FirmwareVersion), string(d.Status),
		d.WarehouseID, d.CustomerID, d.ServiceLocation,
		nullableString(d.IPAddress), nullableString(d.MgmtURI),
		d.LastSeenAt, d.CommissionedAt, d.DecommissionedAt,
		nullableString(d.Notes), d.CreatedAt, d.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "device", "insert device")
	}
	return nil
}

func (r *DeviceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Device, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM netdev.devices WHERE id = $1`, id)
	d, err := scanDevice(row)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepository) FindBySerial(ctx context.Context, serialNo string) (*domain.Device, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM netdev.devices WHERE serial_no = $1`, serialNo)
	d, err := scanDevice(row)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// UpdateLifecycle is the single update path for state-machine
// transitions. Touches every mutable field so each domain method only
// needs to mutate the struct + persist — no per-field UPDATE proliferation.
func (r *DeviceRepository) UpdateLifecycle(ctx context.Context, d *domain.Device) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE netdev.devices SET
			mac_addr = $2,
			asset_tag = $3,
			firmware_version = $4,
			status = $5,
			warehouse_id = $6,
			customer_id = $7,
			service_location_id = $8,
			ip_address = $9,
			mgmt_uri = $10,
			last_seen_at = $11,
			commissioned_at = $12,
			decommissioned_at = $13,
			notes = $14,
			updated_at = NOW()
		WHERE id = $1
	`,
		d.ID,
		nullableString(d.MACAddr), nullableString(d.AssetTag),
		nullableString(d.FirmwareVersion), string(d.Status),
		d.WarehouseID, d.CustomerID, d.ServiceLocation,
		nullableString(d.IPAddress), nullableString(d.MgmtURI),
		d.LastSeenAt, d.CommissionedAt, d.DecommissionedAt,
		nullableString(d.Notes),
	)
	if err != nil {
		return mapDBError(err, "device", "update device lifecycle")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("device.not_found", "device not found")
	}
	return nil
}

func (r *DeviceRepository) List(ctx context.Context, f port.DeviceListFilter) ([]domain.Device, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.Kind != "" {
		args = append(args, f.Kind)
		wh = append(wh, fmt.Sprintf("kind = $%d", len(args)))
	}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if f.WarehouseID != nil {
		args = append(args, *f.WarehouseID)
		wh = append(wh, fmt.Sprintf("warehouse_id = $%d", len(args)))
	}
	if s := strings.TrimSpace(f.SerialLike); s != "" {
		args = append(args, "%"+s+"%")
		wh = append(wh, fmt.Sprintf("serial_no ILIKE $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM netdev.devices`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "device.count", "count devices", err)
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
	sql := `SELECT ` + deviceCols + ` FROM netdev.devices` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "device.list", "list devices", err)
	}
	defer rows.Close()

	out := []domain.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, nil
}

func (r *DeviceRepository) FindFirstAvailable(ctx context.Context, kind domain.DeviceKind, model string, warehouseID *uuid.UUID) (*domain.Device, error) {
	args := []any{string(kind), nullableString(model)}
	clause := "kind = $1 AND status = 'in_stock' AND (model = $2 OR $2 IS NULL)"
	if warehouseID != nil {
		args = append(args, *warehouseID)
		clause += " AND warehouse_id = $3"
	}
	sql := `SELECT ` + deviceCols + ` FROM netdev.devices WHERE ` + clause +
		` ORDER BY created_at ASC LIMIT 1`
	row := r.pool.QueryRow(ctx, sql, args...)
	d, err := scanDevice(row)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func scanDevice(row pgx.Row) (domain.Device, error) {
	var d domain.Device
	var kind, status string
	err := row.Scan(
		&d.ID, &d.SerialNo,
		&d.MACAddr, &d.AssetTag,
		&kind, &d.Model, &d.Manufacturer,
		&d.FirmwareVersion, &status,
		&d.WarehouseID, &d.CustomerID, &d.ServiceLocation,
		&d.IPAddress, &d.MgmtURI,
		&d.LastSeenAt, &d.CommissionedAt, &d.DecommissionedAt,
		&d.Notes,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, derrors.NotFound("device.not_found", "device not found")
	}
	if err != nil {
		return domain.Device{}, derrors.Wrap(derrors.KindInternal, "device.scan", "scan device", err)
	}
	d.Kind = domain.DeviceKind(kind)
	d.Status = domain.DeviceStatus(status)
	return d, nil
}
