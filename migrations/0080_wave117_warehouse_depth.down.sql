-- Wave 117 down — strict reverse of the up migration.

-- Permission cleanup
DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'warehouse'
      AND action IN (
          'item.category.read','item.category.write',
          'serialized.scan','serialized.track',
          'cable.cut','cable.track',
          'consumable.consume','consumable.track',
          'sub_warehouse.read','sub_warehouse.manage',
          'asset.location.read','opname.tablet.sync',
          'qr.generate','qr.scan'
      )
);
DELETE FROM identity.permissions
WHERE module = 'warehouse'
  AND action IN (
      'item.category.read','item.category.write',
      'serialized.scan','serialized.track',
      'cable.cut','cable.track',
      'consumable.consume','consumable.track',
      'sub_warehouse.read','sub_warehouse.manage',
      'asset.location.read','opname.tablet.sync',
      'qr.generate','qr.scan'
  );

DROP TABLE IF EXISTS warehouse.opname_tablet_sessions;
DROP TABLE IF EXISTS warehouse.asset_location_history;
DROP TABLE IF EXISTS warehouse.sub_warehouses;
DROP TABLE IF EXISTS warehouse.batch_consumption_log;
DROP TABLE IF EXISTS warehouse.consumable_batches;
DROP TABLE IF EXISTS warehouse.cable_cuts;
DROP TABLE IF EXISTS warehouse.cable_lots;

ALTER TABLE warehouse.warehouses
    DROP COLUMN IF EXISTS can_purchase;

ALTER TABLE warehouse.assets
    DROP COLUMN IF EXISTS manufactured_date,
    DROP COLUMN IF EXISTS last_movement_at,
    DROP COLUMN IF EXISTS current_location_id;

DROP INDEX IF EXISTS warehouse.uq_stock_items_qr_code;
DROP INDEX IF EXISTS warehouse.idx_stock_items_item_type;

ALTER TABLE warehouse.stock_items
    DROP COLUMN IF EXISTS category_id,
    DROP COLUMN IF EXISTS item_type,
    DROP COLUMN IF EXISTS tracking_kind,
    DROP COLUMN IF EXISTS min_stock_threshold,
    DROP COLUMN IF EXISTS qr_code,
    DROP COLUMN IF EXISTS barcode_format,
    DROP COLUMN IF EXISTS sub_warehouse_allowed;

DROP TABLE IF EXISTS warehouse.item_categories;
