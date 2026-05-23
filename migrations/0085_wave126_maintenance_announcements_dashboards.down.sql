-- Wave 126 down — reverse the additive schema changes. Best-effort:
-- the new tables drop cleanly; the ALTER TABLEs roll back the columns +
-- restore the legacy severity check (and backfill the reverse mapping)
-- so a re-up isn't blocked.

BEGIN;

-- 9. Permissions (delete the new keys; harmless if absent).
DELETE FROM identity.role_permissions
 WHERE permission_id IN (
   SELECT id FROM identity.permissions
    WHERE (module = 'operations.maintenance' AND action IN ('approve','escalate'))
       OR (module = 'operations.announcement' AND action = 'dispatch')
       OR (module = 'operations.calendar' AND action IN ('read','write'))
       OR (module = 'ops.sla' AND action = 'cross_module_view.read')
       OR (module = 'cs.dashboard')
 );

DELETE FROM identity.permissions
 WHERE (module = 'operations.maintenance' AND action IN ('approve','escalate'))
    OR (module = 'operations.announcement' AND action = 'dispatch')
    OR (module = 'operations.calendar' AND action IN ('read','write'))
    OR (module = 'ops.sla' AND action = 'cross_module_view.read')
    OR (module = 'cs.dashboard');

-- 8. CS dashboard aggregations
DROP TABLE IF EXISTS cs.dashboard_aggregations;

-- 7. Cross-module SLA snapshots
DROP TABLE IF EXISTS operations.cross_module_sla_snapshots;

-- 6. Calendar
DROP TABLE IF EXISTS operations.calendar_events;

-- 5. Announcement recipients
DROP TABLE IF EXISTS operations.announcement_recipients;

-- 4. Internal announcements — revert columns + severity check.
ALTER TABLE operations.internal_announcements
    DROP CONSTRAINT IF EXISTS internal_announcements_severity_check;
ALTER TABLE operations.internal_announcements
    DROP CONSTRAINT IF EXISTS internal_announcements_target_audience_check;
ALTER TABLE operations.internal_announcements
    DROP CONSTRAINT IF EXISTS internal_announcements_dispatch_status_check;

ALTER TABLE operations.internal_announcements
    DROP COLUMN IF EXISTS target_audience,
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS dispatched_at,
    DROP COLUMN IF EXISTS dispatch_status;

-- Reverse-backfill severity to the legacy values so the old CHECK passes.
UPDATE operations.internal_announcements
   SET severity = CASE
       WHEN severity = 'important' THEN 'warning'
       WHEN severity = 'urgent'    THEN 'critical'
       WHEN severity NOT IN ('info','warning','critical') THEN 'info'
       ELSE severity
   END
 WHERE severity IS NOT NULL;

ALTER TABLE operations.internal_announcements
    ADD CONSTRAINT internal_announcements_severity_check
    CHECK (severity IN ('info','warning','critical'));

-- 3. Maintenance escalations
DROP TABLE IF EXISTS operations.maintenance_escalations;

-- 2. Maintenance affected customers
DROP TABLE IF EXISTS operations.maintenance_affected_customers;

-- 1. Maintenance event columns
ALTER TABLE field.maintenance_events
    DROP CONSTRAINT IF EXISTS maintenance_events_customer_segment_check;

ALTER TABLE field.maintenance_events
    DROP COLUMN IF EXISTS lead_time_notify_hours,
    DROP COLUMN IF EXISTS customer_segment,
    DROP COLUMN IF EXISTS approval_required,
    DROP COLUMN IF EXISTS approved_by,
    DROP COLUMN IF EXISTS approved_at,
    DROP COLUMN IF EXISTS overrun_at,
    DROP COLUMN IF EXISTS overrun_notified,
    DROP COLUMN IF EXISTS affected_customer_count;

COMMIT;
