-- Wave 123 — Customer Service foundation rollback.

-- Permissions / role_permissions
DELETE FROM identity.role_permissions
 WHERE permission_id IN (
    SELECT id FROM identity.permissions WHERE module = 'cs'
 );
DELETE FROM identity.permissions WHERE module = 'cs';

-- Keep the cs_agent / cs_supervisor roles around so other waves' grants
-- (if any) don't lose their rows. They will just be empty of cs.*
-- permissions after this rollback.

-- Schema cascade drops all 6 cs.* tables in one statement.
DROP SCHEMA IF EXISTS cs CASCADE;
