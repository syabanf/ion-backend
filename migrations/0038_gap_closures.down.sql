-- 0038_gap_closures.down.sql
--
-- Reverts 0038_gap_closures.up.sql. Drops the two new tables
-- (ticket_messages, project_plan_revisions + the catalog),
-- revokes the two new permissions, and removes the three added
-- columns on field.work_orders + field.tickets.
--
-- WARNING: dropping field.ticket_messages loses the conversation
-- timeline for every open ticket. Same for project_plan_revisions —
-- version history is gone. Production rollback should export both.
--
-- The service_catalog rows can be re-seeded by re-running 0038.up.sql
-- (ON CONFLICT DO NOTHING). But dropping the table itself is a
-- destructive op — only do this if downgrading past Wave 1.

BEGIN;

-- 1. Revoke role grants (must run before dropping permissions).
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE p.id = rp.permission_id
  AND (p.module || '.' || p.action) IN (
      'field.ticket.reply',
      'crm.commission.read.own'
  );

-- 2. Drop the two permissions.
DELETE FROM identity.permissions
WHERE (module || '.' || action) IN (
    'field.ticket.reply',
    'crm.commission.read.own'
);

-- 3. Drop the service catalog (indexes drop with the parent).
DROP TABLE IF EXISTS enterprise.service_catalog;

-- 4. Drop project_plan_revisions.
DROP TABLE IF EXISTS enterprise.project_plan_revisions;

-- 5. Drop the convenience column on tickets.
ALTER TABLE field.tickets
    DROP COLUMN IF EXISTS last_message_at;

-- 6. Drop the ticket_messages table (drops the index + CHECK).
DROP TABLE IF EXISTS field.ticket_messages;

-- 7. Drop the two timestamp columns on work_orders.
ALTER TABLE field.work_orders
    DROP COLUMN IF EXISTS arrived_at,
    DROP COLUMN IF EXISTS journey_started_at;

COMMIT;
