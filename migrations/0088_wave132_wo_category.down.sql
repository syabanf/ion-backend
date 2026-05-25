-- Wave 132 down — drop wo_category + its index.

DROP INDEX IF EXISTS field.work_orders_category_idx;
ALTER TABLE field.work_orders DROP COLUMN IF EXISTS wo_category;
