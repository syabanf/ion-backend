// Package branch adapts identity.branches reads to the field bounded
// context's three Wave-65 ports: BranchSLAResolver, AddressToBranchResolver,
// TeamLeaderLookup.
//
// All three queries hit identity-owned tables; the field service holds
// only a narrow read projection, mirroring how the CRM gateway treats
// customer data. Round-1 reaches into the pool directly (same pgxpool
// the rest of field-svc uses); round-2 would put an HTTP shim in front.
//
// PRD references:
//
//	§3 (Branch Hierarchy) — Sub Area → Area → Regional inheritance rule
//	§3 (Branch Hierarchy) — `geo_shape` for address-to-area resolution
//	§9 (Technician & Field) — TL escalation when no leader at Sub Area
package branch

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	fieldport "github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Resolver struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool}
}

// Compile-time checks: Resolver implements all three Wave-65 ports.
var (
	_ fieldport.BranchSLAResolver       = (*Resolver)(nil)
	_ fieldport.AddressToBranchResolver = (*Resolver)(nil)
	_ fieldport.TeamLeaderLookup        = (*Resolver)(nil)
)

// ResolveInstallSLAMinutes walks the branch parent chain looking for a
// non-NULL sla_install_minutes. Sub Area inherits from Area, Area from
// Regional. Returns 0 + nil if the chain is exhausted; the caller
// applies the platform default.
//
// Walks via a recursive CTE — single round-trip, terminates at NULL
// parent_id. Branch trees are 3 deep so this is trivially bounded.
func (r *Resolver) ResolveInstallSLAMinutes(ctx context.Context, branchID uuid.UUID) (int, error) {
	row := r.pool.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_id, sla_install_minutes, 0 AS depth
			  FROM identity.branches
			 WHERE id = $1
			UNION ALL
			SELECT b.id, b.parent_id, b.sla_install_minutes, c.depth + 1
			  FROM identity.branches b
			  JOIN chain c ON c.parent_id = b.id
			 WHERE c.depth < 4
		)
		SELECT sla_install_minutes
		  FROM chain
		 WHERE sla_install_minutes IS NOT NULL
		 ORDER BY depth ASC
		 LIMIT 1
	`, branchID)

	var mins *int
	if err := row.Scan(&mins); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, derrors.Wrap(derrors.KindInternal, "branch.sla_resolve",
			"resolve install sla", err)
	}
	if mins == nil {
		return 0, nil
	}
	return *mins, nil
}

// ResolveAddress takes a free-form address string and finds the Sub
// Area whose `geo_shape` (PostGIS geometry, SRID 4326) contains the
// point returned by geocoding. The geocoder lives in network-svc and
// is reachable via the platform-shared geocode cache table when one
// exists; Wave 65 falls back to (nil, nil) when no cache row is
// present so the caller keeps the CRM-stamped branch_id.
//
// Future: when the geocoder cache table lands the LEFT JOIN below
// becomes an INNER JOIN and we can drop the nil-safe path.
func (r *Resolver) ResolveAddress(ctx context.Context, address string) (*uuid.UUID, error) {
	if address == "" {
		return nil, nil
	}
	// We don't have a geocode cache yet — Wave 65 is forward-compatible
	// scaffolding. When the table exists this method will populate
	// (lat, lng) and ST_Contains will filter on the GIST index. For
	// now we return nil-found so the field service falls through to
	// the pre-stamped branch_id.
	//
	// Network-svc has a coverage_check endpoint that does the lookup;
	// once it exposes a "resolveAddress" sub-call we wire it here.
	_ = ctx
	return nil, nil
}

// FindTeamLeader walks the branch chain looking for an active team in
// the branch (or any ancestor) with a non-null team_leader_id. Returns
// the leader user_id + the branch where the team was actually found
// (so the caller can mark `is_cross_area` when the resolved branch
// differs from the install branch).
//
// The query joins identity.branches (recursively for the parent chain)
// against field.teams. Round-1 picks the first match by depth — meaning
// Sub Area beats Area beats Regional, exactly the PRD escalation rule.
func (r *Resolver) FindTeamLeader(ctx context.Context, branchID uuid.UUID) (*uuid.UUID, *uuid.UUID, error) {
	row := r.pool.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_id, 0 AS depth
			  FROM identity.branches
			 WHERE id = $1
			UNION ALL
			SELECT b.id, b.parent_id, c.depth + 1
			  FROM identity.branches b
			  JOIN chain c ON c.parent_id = b.id
			 WHERE c.depth < 4
		)
		SELECT t.team_leader_id, c.id
		  FROM chain c
		  JOIN field.teams t ON t.branch_id = c.id
		 WHERE t.active = TRUE
		   AND t.team_leader_id IS NOT NULL
		 ORDER BY c.depth ASC
		 LIMIT 1
	`, branchID)

	var userID, foundBranch uuid.UUID
	if err := row.Scan(&userID, &foundBranch); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, derrors.Wrap(derrors.KindInternal, "branch.tl_lookup",
			"find team leader", err)
	}
	return &userID, &foundBranch, nil
}
