-- 0040_priority_followups.down.sql
--
-- Reverts 0040_priority_followups.up.sql. Drops the four new tables
-- (payment intents, device tokens, tech locations, HRIS sync state)
-- and revokes the four new permissions. Re-running the up migration
-- after this completes successfully — no orphaned schema.
--
-- WARNING: dropping billing.payment_intents deletes recorded payment
-- intent rows. Production rollback should ensure those are exported
-- or archived first.

BEGIN;

-- 1. Revoke role grants (must run before dropping the permissions).
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE p.id = rp.permission_id
  AND (p.module || '.' || p.action) IN (
      'billing.invoice.pay',
      'field.tech_location.write',
      'field.tech_location.read',
      'platform.device_token.register'
  );

-- 2. Drop the four permissions.
DELETE FROM identity.permissions
WHERE (module || '.' || action) IN (
    'billing.invoice.pay',
    'field.tech_location.write',
    'field.tech_location.read',
    'platform.device_token.register'
);

-- 3. Drop tables (indexes drop with the parent).
DROP TABLE IF EXISTS identity.hris_sync_state;
DROP TABLE IF EXISTS field.tech_locations;
DROP TABLE IF EXISTS platform.device_tokens;
DROP TABLE IF EXISTS billing.payment_intents;

COMMIT;
