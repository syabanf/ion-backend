-- Wave 111 — rollback for the Payment Service bounded context.
--
-- DROP SCHEMA payment CASCADE takes down all seven tables in one shot.
-- The finance_admin / finance_viewer roles are left in place because
-- other modules may grant them permissions outside this wave; clean up
-- by hand if a hard rollback is required.

BEGIN;

DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE rp.permission_id = p.id
  AND p.module = 'payment';

DELETE FROM identity.permissions
WHERE module = 'payment';

DROP SCHEMA IF EXISTS payment CASCADE;

COMMIT;
