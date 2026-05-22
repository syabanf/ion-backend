-- 0005 — PostGIS extension + coverage polygons on network.nodes.
--
-- Why now (not in 0001): the identity schema doesn't need spatial data;
-- branches store their geo_polygon as plain jsonb (small, infrequent, no
-- spatial queries). Network coverage IS spatial — we'll be running
-- "which ODP covers this lat/lng?" thousands of times per day.
--
-- PostGIS gives us:
--   - geometry(Polygon, 4326)  — WGS84 lat/lng coordinates, same as GPS
--   - GIST spatial index        — sub-millisecond ST_Contains lookups
--   - ST_DWithin on geography   — accurate distance-on-sphere checks
--
-- We add the polygon column to network.nodes (the ODP's coverage shape)
-- and convert branches.geo_polygon to a real PostGIS column too while we
-- have the tooling, so address→branch resolution can use spatial queries
-- later.

BEGIN;

CREATE EXTENSION IF NOT EXISTS postgis;

-- ----------------------------------------------------------------------
-- Coverage polygon on network.nodes
-- ----------------------------------------------------------------------
ALTER TABLE network.nodes
    ADD COLUMN IF NOT EXISTS coverage_polygon geometry(Polygon, 4326);

-- GIST index: PostGIS's R-tree variant. Enables fast spatial joins
-- (ST_Contains, ST_Intersects, ST_DWithin).
CREATE INDEX IF NOT EXISTS idx_network_nodes_coverage_gist
    ON network.nodes USING GIST (coverage_polygon)
    WHERE coverage_polygon IS NOT NULL;

-- A `point` derived column for distance queries. Generated, not stored:
-- we project gps_lat / gps_lng on the fly. Use a btree-based index on
-- the geography type so ST_DWithin can use it. Wrapped in a partial
-- index so nodes without GPS don't bloat it.
CREATE INDEX IF NOT EXISTS idx_network_nodes_point_geog
    ON network.nodes USING GIST (
        (
            ST_SetSRID(ST_MakePoint(gps_lng, gps_lat), 4326)::geography
        )
    )
    WHERE gps_lat IS NOT NULL AND gps_lng IS NOT NULL;

-- ----------------------------------------------------------------------
-- Branches: promote geo_polygon (currently jsonb) to a real geometry column.
--
-- We keep the old jsonb column for the moment (renamed) so any seed
-- data isn't lost; new code reads/writes geo_shape. A follow-up cleanup
-- migration can drop geo_polygon_legacy once nothing references it.
-- ----------------------------------------------------------------------
ALTER TABLE identity.branches
    RENAME COLUMN geo_polygon TO geo_polygon_legacy;

ALTER TABLE identity.branches
    ADD COLUMN IF NOT EXISTS geo_shape geometry(MultiPolygon, 4326);

CREATE INDEX IF NOT EXISTS idx_branches_geo_shape_gist
    ON identity.branches USING GIST (geo_shape)
    WHERE geo_shape IS NOT NULL;

COMMIT;
