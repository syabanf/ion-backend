-- Wave 96 — rollback. Drop the schedule-history table first because it
-- FK-cascades from ewos.id; then strip the new columns + indexes.

BEGIN;

-- Permissions / role grants
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE rp.permission_id = p.id
  AND p.module = 'enterprise'
  AND p.action IN ('tl_scheduling.read', 'tl_scheduling.write', 'ewo.dual.read');

DELETE FROM identity.permissions
WHERE module = 'enterprise'
  AND action IN ('tl_scheduling.read', 'tl_scheduling.write', 'ewo.dual.read');

-- Wave 96 added the team_lead role; we leave it in place on down-migrate
-- because the broader enterprise scaffolding (PRD) calls for the role
-- regardless of this wave's scheduling surface. If a clean rollback is
-- needed, drop it manually.

-- Schedule history table
DROP INDEX IF EXISTS enterprise.idx_ewo_schedule_history_ewo;
DROP TABLE IF EXISTS enterprise.ewo_schedule_history;

-- Indexes
DROP INDEX IF EXISTS enterprise.idx_ewos_executing_side_status;
DROP INDEX IF EXISTS enterprise.idx_ewos_paired;
DROP INDEX IF EXISTS enterprise.idx_ewos_team_lead_schedule;
DROP INDEX IF EXISTS enterprise.idx_ewos_technician_schedule;

-- Columns on enterprise.ewos
ALTER TABLE enterprise.ewos
    DROP COLUMN IF EXISTS schedule_locked,
    DROP COLUMN IF EXISTS assigned_team_lead_user_id,
    DROP COLUMN IF EXISTS assigned_technician_user_id,
    DROP COLUMN IF EXISTS duration_days,
    DROP COLUMN IF EXISTS scheduled_end_date,
    DROP COLUMN IF EXISTS scheduled_start_date,
    DROP COLUMN IF EXISTS paired_ewo_id,
    DROP COLUMN IF EXISTS intercompany_po_id,
    DROP COLUMN IF EXISTS executing_subsidiary_id,
    DROP COLUMN IF EXISTS side;

COMMIT;
