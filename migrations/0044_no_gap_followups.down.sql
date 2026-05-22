-- 0044_no_gap_followups.down.sql
--
-- Reverts 0044_no_gap_followups.up.sql. Drops the four new tables
-- (rate_limit_log, sales_routing_config, ewo_completion_log,
-- dashboard_snapshots), the `opname` schema we created, restores
-- the lead_events.kind comment, and revokes the two new permissions.
--
-- WARNING: dropping ewo_completion_log loses the un-derived vendor
-- metrics signal — anything not yet rolled into vendor_metrics is
-- gone. Production rollback should derive first.

BEGIN;

-- 1. Revoke role grants.
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE p.id = rp.permission_id
  AND (p.module || '.' || p.action) IN (
      'platform.webhook_delivery.read',
      'warehouse.opname.read.rollup'
  );

-- 2. Drop the two new permissions.
DELETE FROM identity.permissions
WHERE (module || '.' || action) IN (
    'platform.webhook_delivery.read',
    'warehouse.opname.read.rollup'
);

-- 3. Restore the lead_events.kind comment (was set by 0041, which
-- doesn't include 'coverage_checked'). The up migration only
-- extended the well-known list, didn't add a CHECK constraint, so
-- the data itself doesn't need touching.
COMMENT ON COLUMN crm.lead_events.kind IS
    'Event kind. Known values: created | status_change | doc_uploaded | '
    'note | sales_reassigned | branch_reassigned | accept_excess_changed | '
    'product_changed | converted';

-- 4. Drop the four new tables (indexes drop with parents).
DROP TABLE IF EXISTS opname.dashboard_snapshots;
DROP TABLE IF EXISTS enterprise.ewo_completion_log;
DROP TABLE IF EXISTS crm.sales_routing_config;
DROP TABLE IF EXISTS platform.rate_limit_log;

-- 5. Drop the opname schema (only if it's now empty — keep IF EXISTS
-- semantics so a re-run doesn't trip).
DROP SCHEMA IF EXISTS opname CASCADE;

COMMIT;
