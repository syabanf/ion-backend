ALTER TABLE warehouse.assets
    DROP COLUMN IF EXISTS purchase_order_id;
DROP TABLE IF EXISTS warehouse.purchase_order_lines;
DROP TABLE IF EXISTS warehouse.purchase_orders;
