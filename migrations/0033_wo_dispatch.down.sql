-- 0033 — DOWN
BEGIN;

DROP TRIGGER IF EXISTS trg_wo_dispatch_records_touch ON warehouse.wo_dispatch_records;

DROP INDEX IF EXISTS warehouse.uq_wo_dispatch_items_scan;
DROP INDEX IF EXISTS warehouse.idx_wo_dispatch_items_item;
DROP INDEX IF EXISTS warehouse.idx_wo_dispatch_items_dispatch_status;
DROP INDEX IF EXISTS warehouse.idx_wo_dispatch_status_planned_at;
DROP INDEX IF EXISTS warehouse.idx_wo_dispatch_warehouse_status;
DROP INDEX IF EXISTS warehouse.idx_wo_dispatch_wo;

DROP TABLE IF EXISTS warehouse.wo_dispatch_items;
DROP TABLE IF EXISTS warehouse.wo_dispatch_records;

DELETE FROM identity.permissions
WHERE module = 'warehouse'
  AND action IN ('dispatch.read','dispatch.manage','dispatch.scan');

COMMIT;
