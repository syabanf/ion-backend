-- Wave 128D down — reverse the additive schema + permission changes.
--
-- The DROP COLUMN cascades into the partial unique index automatically,
-- but we DROP INDEX first explicitly so the rollback log is readable.

BEGIN;

-- 2. Permissions
DELETE FROM identity.role_permissions
 WHERE permission_id IN (
   SELECT id FROM identity.permissions
    WHERE module = 'cs.importer' AND action = 'run'
 );

DELETE FROM identity.permissions
 WHERE module = 'cs.importer' AND action = 'run';

-- 1. Schema
DROP INDEX IF EXISTS cs.uniq_cs_tickets_legacy_id;
ALTER TABLE cs.tickets DROP COLUMN IF EXISTS legacy_id;

COMMIT;
