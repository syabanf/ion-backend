DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'field' AND action IN ('wo.reschedule','sla.read')
);
DELETE FROM identity.permissions
WHERE module = 'field' AND action IN ('wo.reschedule','sla.read');

ALTER TABLE field.bast_records
    DROP COLUMN IF EXISTS otp_code,
    DROP COLUMN IF EXISTS otp_verified_at;

DROP INDEX IF EXISTS field.idx_wo_sla_open;

ALTER TABLE field.work_orders
    DROP COLUMN IF EXISTS sla_due_at;
