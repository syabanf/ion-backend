ALTER TABLE warehouse.wo_dispatch_records DROP COLUMN IF EXISTS source_bom_template_id;
DROP TABLE IF EXISTS warehouse.product_bom_template_items;
DROP TABLE IF EXISTS warehouse.product_bom_templates;
