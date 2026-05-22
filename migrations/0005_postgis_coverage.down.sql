-- 0005 — DOWN
BEGIN;

DROP INDEX IF EXISTS identity.idx_branches_geo_shape_gist;
ALTER TABLE identity.branches DROP COLUMN IF EXISTS geo_shape;
ALTER TABLE identity.branches RENAME COLUMN geo_polygon_legacy TO geo_polygon;

DROP INDEX IF EXISTS network.idx_network_nodes_point_geog;
DROP INDEX IF EXISTS network.idx_network_nodes_coverage_gist;
ALTER TABLE network.nodes DROP COLUMN IF EXISTS coverage_polygon;

DROP EXTENSION IF EXISTS postgis CASCADE;

COMMIT;
