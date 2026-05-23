-- 0066 down — drop the partnership bounded context.
--
-- Schema CASCADE drops every table in one shot; the permission rows are
-- removed by their FK to (module, action) via the matching DELETE.
-- compliance_admin is also removed if it was created here; finance_admin
-- predates this migration so we leave the role row in place (just unbind
-- the partnership perms it picked up).

BEGIN;

-- Drop everything under the schema. CASCADE clears the cross-table FKs
-- (settlements → submissions, submissions → agreements) in one pass.
DROP SCHEMA IF EXISTS partnership CASCADE;

-- Drop the partnership permissions (which auto-cascades the
-- role_permissions rows for super_admin / finance_admin /
-- compliance_admin via the ON DELETE CASCADE in identity.role_permissions).
DELETE FROM identity.permissions WHERE module = 'partnership';

-- Drop the compliance_admin role only if no roles outside this migration
-- have attached to it. finance_admin is preserved (it was seeded in 0002).
DELETE FROM identity.roles WHERE name = 'compliance_admin';

COMMIT;
