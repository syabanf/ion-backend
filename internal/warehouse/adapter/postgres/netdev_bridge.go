// Wave 117 — SQL-only bridge that writes a netdev.devices row when a
// Type 4 (network infra) asset is dispatched. Idempotent on serial_no.
//
// Cross-context call deliberately bypasses the netdev domain layer: we
// only insert/refresh the row, no state-machine traversal. The netdev
// service owns the lifecycle from here on. When netdev is hosted in
// its own binary, swap this for an HTTP adapter without touching the
// warehouse usecase.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/port"
)

type NetdevBridge struct {
	pool *pgxpool.Pool
}

func NewNetdevBridge(pool *pgxpool.Pool) *NetdevBridge {
	return &NetdevBridge{pool: pool}
}

var _ port.NetdevDeviceWriter = (*NetdevBridge)(nil)

// RegisterDevice upserts on serial_no. The netdev.devices.id default is
// uuid_generate_v4(), so an INSERT that hits the SERIAL_NO unique
// constraint resolves via ON CONFLICT to a no-op write — we only update
// the warehouse_id + customer_id pointers on a re-trigger.
func (b *NetdevBridge) RegisterDevice(ctx context.Context, in port.RegisterNetdevInput) error {
	_, err := b.pool.Exec(ctx, `
		INSERT INTO netdev.devices
			(serial_no, mac_addr, asset_tag, kind, model, manufacturer,
			 status, warehouse_id, customer_id)
		VALUES
			($1, NULLIF($2, ''), NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''),
			 CASE WHEN $8::uuid IS NOT NULL THEN 'commissioned' ELSE 'allocated' END,
			 $7, $8)
		ON CONFLICT (serial_no) DO UPDATE
		   SET mac_addr = COALESCE(EXCLUDED.mac_addr, netdev.devices.mac_addr),
		       warehouse_id = EXCLUDED.warehouse_id,
		       customer_id = EXCLUDED.customer_id,
		       updated_at = NOW()
	`, in.SerialNo, in.MACAddr, in.AssetTag, in.Kind, in.Model, in.Manufacturer,
		in.WarehouseID, in.CustomerID)
	return mapDBError(err, "netdev.bridge", "register netdev device")
}
