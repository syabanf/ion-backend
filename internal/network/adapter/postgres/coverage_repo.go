package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type CoverageRepository struct {
	pool *pgxpool.Pool
}

func NewCoverageRepository(pool *pgxpool.Pool) *CoverageRepository {
	return &CoverageRepository{pool: pool}
}

var _ port.CoverageRepository = (*CoverageRepository)(nil)

// FindContaining — ST_Contains on the coverage_polygon column. Uses the
// GIST index we created in migration 0005, so this is fast even with
// thousands of polygons.
//
// Filtering "must be an ODP" via node_type → 'odp'. Other types (POP/OLT)
// can have polygons too but only ODP is what customers attach to.
func (r *CoverageRepository) FindContaining(ctx context.Context, lat, lng float64, onlyAvailable bool) ([]port.CoverageCandidateRow, error) {
	availFilter := ""
	if onlyAvailable {
		// Subquery checks that at least one customer_drop port is available.
		availFilter = `
		  AND EXISTS (
		    SELECT 1 FROM network.ports p
		    WHERE p.node_id = n.id AND p.status = 'available'
		  )
		`
	}

	rows, err := r.pool.Query(ctx, `
		SELECT
		  n.id, n.name, n.code, n.gps_lat, n.gps_lng,
		  COALESCE((SELECT COUNT(*) FROM network.ports p WHERE p.node_id = n.id AND p.status = 'available'), 0)
		FROM network.nodes n
		JOIN network.node_types t ON t.id = n.node_type_id
		WHERE t.type_key = 'odp'
		  AND n.active = TRUE
		  AND n.coverage_polygon IS NOT NULL
		  AND ST_Contains(n.coverage_polygon, ST_SetSRID(ST_MakePoint($1, $2), 4326))
		`+availFilter, lng, lat)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.cov_contain", "ST_Contains query", err)
	}
	defer rows.Close()

	out := []port.CoverageCandidateRow{}
	for rows.Next() {
		var c port.CoverageCandidateRow
		if err := rows.Scan(&c.NodeID, &c.NodeName, &c.NodeCode, &c.GPSLat, &c.GPSLng, &c.AvailablePorts); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.cov_contain_scan", "scan", err)
		}
		c.InPolygon = true
		out = append(out, c)
	}
	return out, nil
}

// FindNearestODPs — ST_DWithin on geography (uses the spatial GIST index)
// for a cheap pre-filter, then ST_Distance for the precise sort order.
//
// We pass the search radius in METERS — geography units. PostGIS does the
// spherical math; accuracy is well within 1m at this scale.
func (r *CoverageRepository) FindNearestODPs(ctx context.Context, lat, lng float64, searchRadiusM int, limit int, onlyAvailable bool) ([]port.CoverageCandidateRow, error) {
	if limit <= 0 {
		limit = 5
	}
	if searchRadiusM <= 0 {
		searchRadiusM = 5000 // 5km default search window — plenty for "nearest ODP"
	}

	availFilter := ""
	if onlyAvailable {
		availFilter = `
		  AND EXISTS (
		    SELECT 1 FROM network.ports p
		    WHERE p.node_id = n.id AND p.status = 'available'
		  )
		`
	}

	rows, err := r.pool.Query(ctx, `
		WITH q AS (
		  SELECT ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography AS pt
		)
		SELECT
		  n.id, n.name, n.code, n.gps_lat, n.gps_lng,
		  ST_Distance(ST_SetSRID(ST_MakePoint(n.gps_lng, n.gps_lat), 4326)::geography, q.pt) AS dist_m,
		  COALESCE((SELECT COUNT(*) FROM network.ports p WHERE p.node_id = n.id AND p.status = 'available'), 0)
		FROM network.nodes n
		JOIN network.node_types t ON t.id = n.node_type_id
		CROSS JOIN q
		WHERE t.type_key = 'odp'
		  AND n.active = TRUE
		  AND n.gps_lat IS NOT NULL AND n.gps_lng IS NOT NULL
		  AND ST_DWithin(
		    ST_SetSRID(ST_MakePoint(n.gps_lng, n.gps_lat), 4326)::geography,
		    q.pt,
		    $3
		  )
		`+availFilter+`
		ORDER BY dist_m ASC
		LIMIT $4
	`, lng, lat, searchRadiusM, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.cov_nearest", "nearest ODP query", err)
	}
	defer rows.Close()

	out := []port.CoverageCandidateRow{}
	for rows.Next() {
		var c port.CoverageCandidateRow
		if err := rows.Scan(&c.NodeID, &c.NodeName, &c.NodeCode, &c.GPSLat, &c.GPSLng, &c.StraightLineM, &c.AvailablePorts); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.cov_nearest_scan", "scan", err)
		}
		out = append(out, c)
	}
	return out, nil
}

// GetPolygon reads the coverage polygon as GeoJSON. Returns nil-pointer
// when the node has no polygon set.
func (r *CoverageRepository) GetPolygon(ctx context.Context, nodeID uuid.UUID) (*domain.GeoJSONPolygon, error) {
	var (
		geoJSON []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT ST_AsGeoJSON(coverage_polygon)::bytea
		FROM network.nodes
		WHERE id = $1 AND coverage_polygon IS NOT NULL
	`, nodeID).Scan(&geoJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.poly_get", "read polygon", err)
	}
	var poly domain.GeoJSONPolygon
	if err := json.Unmarshal(geoJSON, &poly); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.poly_decode", "decode geojson", err)
	}
	return &poly, nil
}

// SavePolygon writes a polygon via ST_GeomFromGeoJSON. PostGIS validates
// the geometry on conversion — invalid polygons (self-intersecting,
// non-closed, wrong winding) come back as a Postgres error which we map
// to Validation.
func (r *CoverageRepository) SavePolygon(ctx context.Context, nodeID uuid.UUID, polygon domain.GeoJSONPolygon) error {
	geoJSON, err := json.Marshal(polygon)
	if err != nil {
		return derrors.Wrap(derrors.KindValidation, "polygon.encode", "encode polygon", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE network.nodes
		SET coverage_polygon = ST_SetSRID(ST_GeomFromGeoJSON($2), 4326)::geometry(Polygon,4326),
		    updated_at = NOW()
		WHERE id = $1
	`, nodeID, string(geoJSON))
	if err != nil {
		// ST_GeomFromGeoJSON / ST_Multi will raise on malformed shapes.
		return derrors.Wrap(derrors.KindValidation, "polygon.invalid", "polygon is invalid", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("node.not_found", "node not found")
	}
	return nil
}

func (r *CoverageRepository) ClearPolygon(ctx context.Context, nodeID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE network.nodes
		SET coverage_polygon = NULL, updated_at = NOW()
		WHERE id = $1
	`, nodeID)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.poly_clear", "clear polygon", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("node.not_found", "node not found")
	}
	return nil
}
