-- 0021_node_type_coverage.up.sql
--
-- Adds `has_coverage_area` to network.node_types so admins can mark
-- which node types carry a service-area polygon. PRD §5.2 designs
-- node types as configurable; previously the FE hardcoded the
-- polygon-capable set (odp/odc/pop) — now that decision lives in the
-- database alongside the type itself.
--
-- Why a boolean and not a more elaborate config? The only thing the FE
-- needs to know is "should I offer this type in the polygon drawer +
-- KMZ importer and fetch a polygon for it on the map?" — that's a
-- one-bit question. If we ever grow to per-type max-radius or polygon
-- styling, this column can be promoted to a JSONB without breaking
-- callers (NULL → "no coverage area").

BEGIN;

ALTER TABLE network.node_types
    ADD COLUMN has_coverage_area BOOLEAN NOT NULL DEFAULT FALSE;

-- Seed defaults per PRD's distribution-tree model:
--   ODP — customer-facing coverage area (find_nearest_available_odp)
--   ODC — sub-region this cabinet's downstream ODPs cover
--   POP — service area for this physical site
-- Everything else (Internet Source, OLT, Splitter, ONT, MikroTik,
-- Switch, Router, Other) is a point device — no polygon.
UPDATE network.node_types
   SET has_coverage_area = TRUE
 WHERE type_key IN ('odp', 'odc', 'pop');

COMMIT;
