package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ImpactRepository struct {
	pool *pgxpool.Pool
}

func NewImpactRepository(pool *pgxpool.Pool) *ImpactRepository {
	return &ImpactRepository{pool: pool}
}

var _ port.ImpactRepository = (*ImpactRepository)(nil)

// Downstream returns every node reachable by walking parent_id from rootID
// (inclusive of root), plus the count of distinct ACTIVE customers attached
// to ports on any of those nodes.
//
// PRD fault-impact rule (§5.5):
//   When a node is reported Down → traverse all descendants → list
//   affected customers.
//
// We cap depth at 8 — even with the worst-case ODC nesting in the PRD's
// example, depth stays well under that; it's a safety belt against
// pathological data.
func (r *ImpactRepository) Downstream(ctx context.Context, rootID uuid.UUID) ([]port.ImpactRow, int, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE tree AS (
		  SELECT n.id, n.name, n.code, n.node_type_id, n.parent_id, 0 AS depth
		  FROM network.nodes n
		  WHERE n.id = $1

		  UNION ALL

		  SELECT c.id, c.name, c.code, c.node_type_id, c.parent_id, t.depth + 1
		  FROM network.nodes c
		  JOIN tree t ON c.parent_id = t.id
		  WHERE t.depth < 8
		)
		SELECT
		  tree.id, tree.name, tree.code, tree.depth, tree.parent_id,
		  COALESCE(p.name, '') AS parent_name,
		  nt.type_key, nt.label
		FROM tree
		JOIN network.node_types nt ON nt.id = tree.node_type_id
		LEFT JOIN network.nodes p  ON p.id = tree.parent_id
		ORDER BY tree.depth, tree.name
	`, rootID)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.impact_tree", "downstream tree", err)
	}
	defer rows.Close()

	out := []port.ImpactRow{}
	for rows.Next() {
		var x port.ImpactRow
		if err := rows.Scan(&x.NodeID, &x.Name, &x.Code, &x.Depth, &x.ParentID, &x.ParentName, &x.NodeTypeKey, &x.NodeTypeLabel); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "db.impact_scan", "scan", err)
		}
		out = append(out, x)
	}

	// Count distinct active customers on ports under the impacted tree.
	// We re-run the recursive CTE inside the count query — keeps the round-
	// trip count to 2 and avoids materializing the tree to pass back.
	var customers int
	if err := r.pool.QueryRow(ctx, `
		WITH RECURSIVE tree AS (
		  SELECT id FROM network.nodes WHERE id = $1
		  UNION ALL
		  SELECT c.id FROM network.nodes c JOIN tree t ON c.parent_id = t.id
		)
		SELECT COUNT(DISTINCT p.customer_id)
		FROM network.ports p
		WHERE p.node_id IN (SELECT id FROM tree)
		  AND p.status = 'active'
		  AND p.customer_id IS NOT NULL
	`, rootID).Scan(&customers); err != nil {
		return out, 0, derrors.Wrap(derrors.KindInternal, "db.impact_customers", "count customers", err)
	}
	return out, customers, nil
}
