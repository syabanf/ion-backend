-- 0001 — Platform Core — DOWN
BEGIN;

DROP TRIGGER IF EXISTS trg_users_touch ON identity.users;
DROP TRIGGER IF EXISTS trg_branches_touch ON identity.branches;
DROP FUNCTION IF EXISTS identity.touch_updated_at();

DROP TABLE IF EXISTS identity.platform_config;
DROP TABLE IF EXISTS identity.audit_logs;
DROP TABLE IF EXISTS identity.user_roles;
DROP TABLE IF EXISTS identity.role_permissions;
DROP TABLE IF EXISTS identity.permissions;
DROP TABLE IF EXISTS identity.roles;
DROP TABLE IF EXISTS identity.technician_profiles;
DROP TABLE IF EXISTS identity.sales_rep_profiles;
DROP TABLE IF EXISTS identity.users;
DROP TABLE IF EXISTS identity.branches;

DROP SCHEMA IF EXISTS identity;

COMMIT;
