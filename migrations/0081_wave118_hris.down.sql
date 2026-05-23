-- Wave 118 — HRIS Integration rollback.

-- Permissions / role_permissions (delete in reverse order — FKs go from
-- role_permissions to roles + permissions, both deleted last)
DELETE FROM identity.role_permissions
 WHERE permission_id IN (
    SELECT id FROM identity.permissions WHERE module = 'hris'
 );

DELETE FROM identity.permissions WHERE module = 'hris';

-- Keep the roles around — they may be referenced by other waves' grants.
-- (No DELETE on identity.roles for hr_admin / finance_admin; they are now
-- empty of hris permissions, and a re-up will re-seed grants.)

-- identity.users — drop FK + column.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'fk_user_hris_employee'
    ) THEN
        ALTER TABLE identity.users
            DROP CONSTRAINT fk_user_hris_employee;
    END IF;
END$$;

DROP INDEX IF EXISTS identity.uq_users_hris_employee_no;
ALTER TABLE identity.users DROP COLUMN IF EXISTS hris_employee_no;

-- hris.* tables — drop in reverse FK order.
DROP TABLE IF EXISTS hris.employee_events;
DROP TABLE IF EXISTS hris.employees;
DROP SCHEMA IF EXISTS hris;
