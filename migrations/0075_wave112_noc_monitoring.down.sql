-- Wave 112 down — drop the entire nocmon bounded context.
--
-- Schema-cascade is safe because nothing outside nocmon FKs into it
-- (cross-context decoupling). Permissions + roles are cleaned up
-- below; we leave the noc_* roles in place (they may be referenced
-- by user_roles rows that an operator would like to retain even if
-- the schema is rolled back).

BEGIN;

DROP SCHEMA IF EXISTS nocmon CASCADE;

DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions WHERE module = 'nocmon'
);

DELETE FROM identity.permissions
WHERE module = 'nocmon';

COMMIT;
