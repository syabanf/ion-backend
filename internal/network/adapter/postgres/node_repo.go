package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type NodeRepository struct {
	pool *pgxpool.Pool
}

func NewNodeRepository(pool *pgxpool.Pool) *NodeRepository {
	return &NodeRepository{pool: pool}
}

var _ port.NodeRepository = (*NodeRepository)(nil)

// listSelect joins lookups needed for the table view in one query.
// COALESCE empty defaults so the scan never sees NULL when we don't want it.
const listSelectPrefix = `
SELECT
  n.id, n.node_type_id, n.name, n.code, n.parent_id, n.upstream_port_id,
  n.branch_id, n.asset_id, COALESCE(n.address,''),
  n.gps_lat, n.gps_lng, n.coverage_radius_m, n.total_ports,
  n.status, n.metadata, n.active, n.created_at, n.updated_at,
  t.type_key, t.label,
  COALESCE(b.name,''), COALESCE(b.code,''),
  COALESCE(p.name,''),
  COALESCE((SELECT COUNT(*) FROM network.ports x WHERE x.node_id = n.id), 0) AS ports_total,
  COALESCE((SELECT COUNT(*) FROM network.ports x WHERE x.node_id = n.id AND x.status IN ('reserved','active')), 0) AS ports_used
FROM network.nodes n
JOIN network.node_types t ON t.id = n.node_type_id
LEFT JOIN identity.branches b ON b.id = n.branch_id
LEFT JOIN network.nodes p ON p.id = n.parent_id
`

func (r *NodeRepository) List(ctx context.Context, f port.NodeListFilter) ([]port.NodeListItem, int, error) {
	conds := []string{"1=1"}
	args := []any{}
	idx := 1

	if s := strings.TrimSpace(f.Search); s != "" {
		conds = append(conds, fmt.Sprintf("(n.name ILIKE $%d OR n.code ILIKE $%d)", idx, idx))
		args = append(args, "%"+s+"%")
		idx++
	}
	if f.NodeTypeID != nil {
		conds = append(conds, fmt.Sprintf("n.node_type_id = $%d", idx))
		args = append(args, *f.NodeTypeID)
		idx++
	}
	if f.BranchID != nil {
		conds = append(conds, fmt.Sprintf("n.branch_id = $%d", idx))
		args = append(args, *f.BranchID)
		idx++
	}
	if f.ParentID != nil {
		conds = append(conds, fmt.Sprintf("n.parent_id = $%d", idx))
		args = append(args, *f.ParentID)
		idx++
	}
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf("n.status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.Active != nil {
		conds = append(conds, fmt.Sprintf("n.active = $%d", idx))
		args = append(args, *f.Active)
		idx++
	}
	where := strings.Join(conds, " AND ")

	// Total
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM network.nodes n WHERE `+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.node_count", "count nodes", err)
	}

	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	sql := listSelectPrefix + " WHERE " + where +
		fmt.Sprintf(" ORDER BY t.sort_order, n.name LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.node_list", "list nodes", err)
	}
	defer rows.Close()

	out := []port.NodeListItem{}
	for rows.Next() {
		it, err := scanNodeListItem(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, nil
}

func (r *NodeRepository) FindByID(ctx context.Context, id uuid.UUID) (*port.NodeListItem, error) {
	row := r.pool.QueryRow(ctx, listSelectPrefix+" WHERE n.id = $1", id)
	it, err := scanNodeListItem(row)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// Create inserts the node and, if autoCreatePorts > 0, seeds that many
// ports numbered 1..N with the supplied role. Single transaction.
func (r *NodeRepository) Create(ctx context.Context, n *domain.Node, autoCreatePorts int, defaultPortRole domain.PortRole) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	metaJSON, _ := json.Marshal(n.Metadata)

	if _, err := tx.Exec(ctx, `
		INSERT INTO network.nodes (
			id, node_type_id, name, code, parent_id, upstream_port_id,
			branch_id, asset_id, address, gps_lat, gps_lng,
			coverage_radius_m, total_ports, status, metadata, active,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
		)
	`,
		n.ID, n.NodeTypeID, n.Name, n.Code, n.ParentID, n.UpstreamPortID,
		n.BranchID, n.AssetID, nullableString(n.Address), n.GPSLat, n.GPSLng,
		n.CoverageRadiusM, n.TotalPorts, string(n.Status), metaJSON, n.Active,
		n.CreatedAt, n.UpdatedAt,
	); err != nil {
		return mapInsertError(err, "node", "insert node")
	}

	for i := 1; i <= autoCreatePorts; i++ {
		if _, err := tx.Exec(ctx, `
			INSERT INTO network.ports (node_id, port_number, port_role, status)
			VALUES ($1, $2, $3, 'available')
		`, n.ID, i, string(defaultPortRole)); err != nil {
			return mapInsertError(err, "port", "seed port")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

// Update applies a partial patch; nil fields are left alone, Clear* booleans
// NULL the field. Mirrors the pattern in identity.UserRepository.Update.
func (r *NodeRepository) Update(ctx context.Context, in port.UpdateNodeInput) (*domain.Node, error) {
	sets := []string{}
	args := []any{in.ID}
	idx := 2
	add := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}

	if in.Name != nil {
		add("name", strings.TrimSpace(*in.Name))
	}
	if in.ClearParent {
		sets = append(sets, "parent_id = NULL")
	} else if in.ParentID != nil {
		add("parent_id", *in.ParentID)
	}
	if in.ClearUpstream {
		sets = append(sets, "upstream_port_id = NULL")
	} else if in.UpstreamPortID != nil {
		add("upstream_port_id", *in.UpstreamPortID)
	}
	if in.ClearBranch {
		sets = append(sets, "branch_id = NULL")
	} else if in.BranchID != nil {
		add("branch_id", *in.BranchID)
	}
	if in.Address != nil {
		add("address", *in.Address)
	}
	if in.ClearGPS {
		sets = append(sets, "gps_lat = NULL", "gps_lng = NULL")
	} else if in.GPSLat != nil && in.GPSLng != nil {
		add("gps_lat", *in.GPSLat)
		add("gps_lng", *in.GPSLng)
	}
	if in.ClearCoverage {
		sets = append(sets, "coverage_radius_m = NULL")
	} else if in.CoverageRadiusM != nil {
		add("coverage_radius_m", *in.CoverageRadiusM)
	}
	if in.Status != nil {
		add("status", string(*in.Status))
	}
	if in.Active != nil {
		add("active", *in.Active)
	}
	if in.Metadata != nil {
		metaJSON, _ := json.Marshal(in.Metadata)
		add("metadata", metaJSON)
	}

	if len(sets) == 0 {
		// Nothing to change → just return current state.
		row := r.pool.QueryRow(ctx, `SELECT `+nodeBaseCols+` FROM network.nodes WHERE id = $1`, in.ID)
		return scanNodeBase(row)
	}

	sql := fmt.Sprintf(`UPDATE network.nodes SET %s WHERE id = $1`, strings.Join(sets, ", "))
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, mapInsertError(err, "node", "update node")
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.NotFound("node.not_found", "node not found")
	}

	row := r.pool.QueryRow(ctx, `SELECT `+nodeBaseCols+` FROM network.nodes WHERE id = $1`, in.ID)
	return scanNodeBase(row)
}

// --- internal helpers ---

const nodeBaseCols = `
  id, node_type_id, name, code, parent_id, upstream_port_id,
  branch_id, asset_id, COALESCE(address,''),
  gps_lat, gps_lng, coverage_radius_m, total_ports,
  status, metadata, active, created_at, updated_at
`

func scanNodeBase(row pgx.Row) (*domain.Node, error) {
	var (
		n        domain.Node
		metaJSON []byte
		status   string
	)
	err := row.Scan(
		&n.ID, &n.NodeTypeID, &n.Name, &n.Code, &n.ParentID, &n.UpstreamPortID,
		&n.BranchID, &n.AssetID, &n.Address,
		&n.GPSLat, &n.GPSLng, &n.CoverageRadiusM, &n.TotalPorts,
		&status, &metaJSON, &n.Active, &n.CreatedAt, &n.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("node.not_found", "node not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.node_scan", "scan node", err)
	}
	n.Status = domain.NodeStatus(status)
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &n.Metadata)
	}
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	return &n, nil
}

func scanNodeListItem(row pgx.Row) (port.NodeListItem, error) {
	var (
		n        domain.Node
		metaJSON []byte
		status   string
		it       port.NodeListItem
	)
	err := row.Scan(
		&n.ID, &n.NodeTypeID, &n.Name, &n.Code, &n.ParentID, &n.UpstreamPortID,
		&n.BranchID, &n.AssetID, &n.Address,
		&n.GPSLat, &n.GPSLng, &n.CoverageRadiusM, &n.TotalPorts,
		&status, &metaJSON, &n.Active, &n.CreatedAt, &n.UpdatedAt,
		&it.NodeTypeKey, &it.NodeTypeLabel,
		&it.BranchName, &it.BranchCode,
		&it.ParentName,
		&it.PortsTotal, &it.PortsUsed,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.NodeListItem{}, derrors.NotFound("node.not_found", "node not found")
	}
	if err != nil {
		return port.NodeListItem{}, derrors.Wrap(derrors.KindInternal, "db.node_scan", "scan node", err)
	}
	n.Status = domain.NodeStatus(status)
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &n.Metadata)
	}
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	it.Node = n
	return it, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
