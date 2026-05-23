-- Wave 125 — Down migration.
--
-- Drops the per-item tables + the new bulk_jobs aggregate. Leaves
-- `operations.bulk_operations` (Wave 71) intact. Permissions added by
-- the up migration are removed via cascade on the grant table.

BEGIN;

DROP TABLE IF EXISTS operations.bulk_wo_creation_items;
DROP TABLE IF EXISTS operations.bulk_odp_migration_items;
DROP TABLE IF EXISTS operations.bulk_plan_change_items;
DROP TABLE IF EXISTS operations.bulk_jobs;

-- Permissions added in 0084 — best-effort delete (CASCADE on
-- role_permissions cleans up grants).
DELETE FROM identity.permissions
 WHERE module = 'operations'
   AND action IN (
       'bulk.run','bulk.cancel',
       'bulk_plan_change.run','bulk_odp_migration.run','bulk_wo_creation.run'
   );

-- Leave the ops_lead role intact even on rollback — other waves may
-- have grants attached. Worst case is an empty role.

COMMIT;
