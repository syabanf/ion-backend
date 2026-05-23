-- Wave 84 (QA TC-WO-011) — per-customer WO checklist via product schema.
--
-- Foundation only: capture product_id + service_schema_id on the WO row
-- so subsequent reads can resolve the schema-driven checklist content.
-- Materialization of schema content into checklist items lands in
-- Wave 84b — kept separate because the JSON schema shape requires PRD
-- alignment and proper integration tests.

-- 1. WO carries the product + the pinned service schema version.
--    Both nullable so legacy rows continue to load. FK to crm.products
--    is intentionally ON DELETE SET NULL: deleting a product shouldn't
--    cascade-nuke historical work orders, just orphan them. service
--    schema FK left untyped to platform.schema_definitions because
--    platform may move out-of-process; we only need the UUID for
--    audit + the cross-context resolver.
ALTER TABLE field.work_orders
    ADD COLUMN IF NOT EXISTS product_id UUID
        REFERENCES crm.products(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS service_schema_id UUID;

CREATE INDEX IF NOT EXISTS idx_wo_product_id
    ON field.work_orders (product_id) WHERE product_id IS NOT NULL;

-- 2. Checklist templates can now be scoped per-product (in addition
--    to per-product_type). When `product_id` is set, FindTemplateFor
--    prefers that template; null falls back to the legacy
--    (wo_type, product_type) default. The Wave 84b materializer will
--    write rows here keyed by product_id + derived_from_schema_id.
ALTER TABLE field.wo_checklist_templates
    ADD COLUMN IF NOT EXISTS product_id UUID
        REFERENCES crm.products(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS derived_from_schema_id UUID;

-- Per-product templates must be unique per (wo_type, product_id).
-- The legacy (wo_type, product_type, maintenance_subtype) unique
-- continues to apply when product_id IS NULL — a partial unique index
-- enforces it without disrupting the original constraint.
CREATE UNIQUE INDEX IF NOT EXISTS idx_wo_checklist_tpl_product
    ON field.wo_checklist_templates (wo_type, product_id)
    WHERE product_id IS NOT NULL;
