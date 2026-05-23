package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// HealthSnapshotRepository — netdev.device_health_snapshots
type HealthSnapshotRepository struct {
	pool *pgxpool.Pool
}

func NewHealthSnapshotRepository(pool *pgxpool.Pool) *HealthSnapshotRepository {
	return &HealthSnapshotRepository{pool: pool}
}

var _ port.HealthSnapshotRepository = (*HealthSnapshotRepository)(nil)

func (r *HealthSnapshotRepository) Insert(ctx context.Context, s *domain.HealthSnapshot) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO netdev.device_health_snapshots
			(id, device_id, snapped_at, uptime_seconds,
			 signal_dbm, packet_loss_pct, cpu_pct, memory_pct, raw_payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`,
		s.ID, s.DeviceID, s.SnappedAt,
		s.UptimeSeconds, s.SignalDBM, s.PacketLossPct,
		s.CPUPct, s.MemoryPct, s.RawPayload,
	)
	if err != nil {
		return mapDBError(err, "health_snapshot", "insert health snapshot")
	}
	return nil
}

func (r *HealthSnapshotRepository) ListRecent(ctx context.Context, deviceID uuid.UUID, limit int) ([]domain.HealthSnapshot, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, device_id, snapped_at, uptime_seconds,
		       signal_dbm, packet_loss_pct, cpu_pct, memory_pct, raw_payload
		FROM netdev.device_health_snapshots
		WHERE device_id = $1
		ORDER BY snapped_at DESC
		LIMIT $2
	`, deviceID, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "health_snapshot.list", "list snapshots", err)
	}
	defer rows.Close()
	out := []domain.HealthSnapshot{}
	for rows.Next() {
		var s domain.HealthSnapshot
		if err := rows.Scan(
			&s.ID, &s.DeviceID, &s.SnappedAt, &s.UptimeSeconds,
			&s.SignalDBM, &s.PacketLossPct, &s.CPUPct, &s.MemoryPct, &s.RawPayload,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "health_snapshot.scan", "scan snapshot", err)
		}
		out = append(out, s)
	}
	return out, nil
}

// CountConsecutiveLowScores walks back through the last `lookback`
// snapshots in time order and counts how many scored below threshold.
// We compute the score in Go (not SQL) so the scoring rule stays in
// one place — the domain package.
func (r *HealthSnapshotRepository) CountConsecutiveLowScores(ctx context.Context, deviceID uuid.UUID, threshold, lookback int) (int, error) {
	if lookback <= 0 {
		lookback = 3
	}
	recent, err := r.ListRecent(ctx, deviceID, lookback)
	if err != nil {
		return 0, err
	}
	count := 0
	for i := range recent {
		if domain.ComputeHealthScore(recent[i]) < threshold {
			count++
		}
	}
	return count, nil
}
