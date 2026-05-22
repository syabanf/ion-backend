-- 0021_node_type_coverage.down.sql — drop the has_coverage_area column.
BEGIN;

ALTER TABLE network.node_types DROP COLUMN IF EXISTS has_coverage_area;

COMMIT;
