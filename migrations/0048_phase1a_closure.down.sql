-- Down migration for 0048. Drops only what 0048 added.

BEGIN;

-- Operations permissions (deleted via the (module, action) natural key)
DELETE FROM identity.role_permissions
 WHERE permission_id IN (
   SELECT id FROM identity.permissions WHERE module = 'operations'
 );
DELETE FROM identity.permissions WHERE module = 'operations';

-- maintenance_events escalation columns
ALTER TABLE field.maintenance_events
    DROP COLUMN IF EXISTS escalation_reason,
    DROP COLUMN IF EXISTS war_room_incident_id,
    DROP COLUMN IF EXISTS escalated_to_war_room_at;

-- operations schema tables + indexes
DROP INDEX IF EXISTS operations.idx_announcements_pending;
DROP TABLE IF EXISTS operations.internal_announcements;

DROP INDEX IF EXISTS operations.idx_bulk_ops_branch;
DROP INDEX IF EXISTS operations.idx_bulk_ops_status;
DROP TABLE IF EXISTS operations.bulk_operations;

DROP SCHEMA IF EXISTS operations;

-- identity.branches SLA columns
ALTER TABLE identity.branches
    DROP COLUMN IF EXISTS sla_install_minutes,
    DROP COLUMN IF EXISTS sla_dispatch_minutes,
    DROP COLUMN IF EXISTS sla_assignment_minutes;

-- radius_accounts temp expiry
DROP INDEX IF EXISTS network.idx_radius_temp_expiry;
ALTER TABLE network.radius_accounts
    DROP COLUMN IF EXISTS temp_expires_at;

COMMIT;
