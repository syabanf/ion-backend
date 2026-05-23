-- Wave 84 down — drop the per-product checklist scoping + the WO
-- schema reference columns. Indexes drop with their columns.

ALTER TABLE field.wo_checklist_templates
    DROP COLUMN IF EXISTS derived_from_schema_id,
    DROP COLUMN IF EXISTS product_id;

ALTER TABLE field.work_orders
    DROP COLUMN IF EXISTS service_schema_id,
    DROP COLUMN IF EXISTS product_id;
