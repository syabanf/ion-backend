-- Wave 107 — rollback for the Provider & Vendor Input bounded context.
--
-- We DROP SCHEMA vendor CASCADE to take down all four tables in one
-- shot. The vendor_admin role is left in place because other modules
-- might have granted it permissions outside this wave; clean it up
-- manually if a hard rollback is required.

BEGIN;

-- Permission grants
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE rp.permission_id = p.id
  AND p.module = 'vendor';

DELETE FROM identity.permissions
WHERE module = 'vendor';

DROP SCHEMA IF EXISTS vendor CASCADE;

COMMIT;
