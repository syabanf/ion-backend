-- 0041_p1_p2_followups.down.sql
--
-- Reverts 0041_p1_p2_followups.up.sql. Drops the five new tables
-- (customer_notifications, lead_events, vendor_documents,
-- vendor_metrics, priority_insertions), revokes the seven new
-- permissions, and restores the original leads.source CHECK
-- constraint.
--
-- WARNING: dropping crm.lead_events deletes the entire lead
-- interaction timeline. Production rollback should export first.

BEGIN;

-- 1. Revoke role grants for the seven new permissions.
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE p.id = rp.permission_id
  AND (p.module || '.' || p.action) IN (
      'crm.notification.read',
      'crm.lead_event.read',
      'field.cross_area.request',
      'field.priority_insert',
      'enterprise.vendor_doc.manage',
      'enterprise.vendor_metric.read',
      'warehouse.stock_dashboard.read'
  );

-- 2. Drop the seven permissions.
DELETE FROM identity.permissions
WHERE (module || '.' || action) IN (
    'crm.notification.read',
    'crm.lead_event.read',
    'field.cross_area.request',
    'field.priority_insert',
    'enterprise.vendor_doc.manage',
    'enterprise.vendor_metric.read',
    'warehouse.stock_dashboard.read'
);

-- 3. Drop the five new tables (indexes drop with parent).
DROP TABLE IF EXISTS field.priority_insertions;
DROP TABLE IF EXISTS enterprise.vendor_metrics;
DROP TABLE IF EXISTS enterprise.vendor_documents;
DROP TABLE IF EXISTS crm.lead_events;
DROP TABLE IF EXISTS crm.customer_notifications;

-- 4. Restore the original leads.source CHECK constraint (without
-- 'cs_referral'). NOTE: this will fail if any rows reference
-- 'cs_referral' — clean those up first.
ALTER TABLE crm.leads DROP CONSTRAINT IF EXISTS leads_source_check;
ALTER TABLE crm.leads ADD CONSTRAINT leads_source_check
    CHECK (source IN ('manual','self_order','sales_app','referral'));

COMMIT;
