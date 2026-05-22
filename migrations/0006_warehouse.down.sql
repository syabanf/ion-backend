-- 0006 — DOWN
BEGIN;

DROP TRIGGER IF EXISTS trg_transfers_touch  ON warehouse.transfers;
DROP TRIGGER IF EXISTS trg_assets_touch     ON warehouse.assets;
DROP TRIGGER IF EXISTS trg_stock_items_touch ON warehouse.stock_items;
DROP TRIGGER IF EXISTS trg_warehouses_touch ON warehouse.warehouses;
DROP FUNCTION IF EXISTS warehouse.touch_updated_at();

DROP TABLE IF EXISTS warehouse.transfer_items;
DROP TABLE IF EXISTS warehouse.transfers;
DROP TABLE IF EXISTS warehouse.stock_movements;
DROP TABLE IF EXISTS warehouse.assets;
DROP TABLE IF EXISTS warehouse.stock_levels;
DROP TABLE IF EXISTS warehouse.stock_items;
DROP TABLE IF EXISTS warehouse.warehouses;
DROP SCHEMA IF EXISTS warehouse;

DELETE FROM identity.permissions
WHERE module = 'warehouse'
  AND action IN (
    'warehouse.read','warehouse.manage',
    'catalog.read','catalog.manage',
    'stock.intake','transfer.manage'
  );

COMMIT;
