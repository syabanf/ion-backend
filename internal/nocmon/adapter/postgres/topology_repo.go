package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// TopologySnapshotRepository implements
// port.TopologySnapshotRepository against
// `nocmon.topology_snapshots`. Append-only writes; readers always
// take the latest per (scope, scope_id).
type TopologySnapshotRepository struct {
	pool *pgxpool.Pool
}

func NewTopologySnapshotRepository(pool *pgxpool.Pool) *TopologySnapshotRepository {
	return &TopologySnapshotRepository{pool: pool}
}

var _ port.TopologySnapshotRepository = (*TopologySnapshotRepository)(nil)

func (r *TopologySnapshotRepository) Create(ctx context.Context, s *domain.TopologySnapshot) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO nocmon.topology_snapshots
			(id, scope, scope_id, snapshot_at, payload,
			 node_count, edge_count, generated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		s.ID, string(s.Scope), s.ScopeID, s.SnapshotAt, s.Payload,
		s.NodeCount, s.EdgeCount, nullableString(s.GeneratedBy),
	)
	if err != nil {
		return mapDBError(err, "topology", "insert topology_snapshot")
	}
	return nil
}

func (r *TopologySnapshotRepository) FindLatest(ctx context.Context, scope domain.TopologyScope, scopeID *uuid.UUID) (*domain.TopologySnapshot, error) {
	// scopeID can be nil for the "regional" shape; the IS NOT DISTINCT
	// FROM trick handles nil vs. real-uuid uniformly so we don't need
	// two SQL branches.
	row := r.pool.QueryRow(ctx, `
		SELECT id, scope, scope_id, snapshot_at, payload,
		       node_count, edge_count, COALESCE(generated_by, '')
		FROM nocmon.topology_snapshots
		WHERE scope = $1
		  AND scope_id IS NOT DISTINCT FROM $2
		ORDER BY snapshot_at DESC
		LIMIT 1
	`, string(scope), scopeID)

	var s domain.TopologySnapshot
	var scopeStr string
	if err := row.Scan(
		&s.ID, &scopeStr, &s.ScopeID, &s.SnapshotAt, &s.Payload,
		&s.NodeCount, &s.EdgeCount, &s.GeneratedBy,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("topology.not_found", "no topology snapshot for the given scope")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "db.topology_scan", "scan topology snapshot", err)
	}
	s.Scope = domain.TopologyScope(scopeStr)
	return &s, nil
}
