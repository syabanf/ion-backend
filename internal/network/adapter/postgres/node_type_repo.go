package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type NodeTypeRepository struct {
	pool *pgxpool.Pool
}

func NewNodeTypeRepository(pool *pgxpool.Pool) *NodeTypeRepository {
	return &NodeTypeRepository{pool: pool}
}

var _ port.NodeTypeRepository = (*NodeTypeRepository)(nil)

const nodeTypeCols = `id, type_key, label, COALESCE(description,''), COALESCE(icon_online,''), COALESCE(icon_offline,''), COALESCE(icon_trouble,''), sort_order, active, has_coverage_area, created_at`

func (r *NodeTypeRepository) List(ctx context.Context, includeInactive bool) ([]domain.NodeType, error) {
	sql := `SELECT ` + nodeTypeCols + ` FROM network.node_types`
	if !includeInactive {
		sql += ` WHERE active = TRUE`
	}
	sql += ` ORDER BY sort_order, label`

	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.node_type_list", "list node types", err)
	}
	defer rows.Close()

	out := []domain.NodeType{}
	for rows.Next() {
		var t domain.NodeType
		if err := rows.Scan(&t.ID, &t.TypeKey, &t.Label, &t.Description,
			&t.IconOnline, &t.IconOffline, &t.IconTrouble, &t.SortOrder, &t.Active, &t.HasCoverageArea, &t.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.node_type_scan", "scan node type", err)
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *NodeTypeRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.NodeType, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+nodeTypeCols+` FROM network.node_types WHERE id = $1`, id)
	return scanNodeType(row)
}

func (r *NodeTypeRepository) FindByKey(ctx context.Context, typeKey string) (*domain.NodeType, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+nodeTypeCols+` FROM network.node_types WHERE type_key = $1`, typeKey)
	return scanNodeType(row)
}

func (r *NodeTypeRepository) Create(ctx context.Context, t *domain.NodeType) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO network.node_types
			(id, type_key, label, description, icon_online, icon_offline, icon_trouble, sort_order, active, has_coverage_area, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, t.ID, t.TypeKey, t.Label, t.Description, t.IconOnline, t.IconOffline, t.IconTrouble, t.SortOrder, t.Active, t.HasCoverageArea, t.CreatedAt)
	if err != nil {
		return mapInsertError(err, "node_type", "insert node type")
	}
	return nil
}

func (r *NodeTypeRepository) Update(ctx context.Context, t *domain.NodeType) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE network.node_types
		SET label = $2, description = $3,
		    icon_online = $4, icon_offline = $5, icon_trouble = $6,
		    sort_order = $7, active = $8, has_coverage_area = $9
		WHERE id = $1
	`, t.ID, t.Label, t.Description, t.IconOnline, t.IconOffline, t.IconTrouble, t.SortOrder, t.Active, t.HasCoverageArea)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.node_type_update", "update node type", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("node_type.not_found", "node type not found")
	}
	return nil
}

func scanNodeType(row pgx.Row) (*domain.NodeType, error) {
	var t domain.NodeType
	err := row.Scan(&t.ID, &t.TypeKey, &t.Label, &t.Description,
		&t.IconOnline, &t.IconOffline, &t.IconTrouble, &t.SortOrder, &t.Active, &t.HasCoverageArea, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("node_type.not_found", "node type not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.node_type_scan", "scan node type", err)
	}
	return &t, nil
}
