-- M5 Round 2 — Reschedule UI, OTP-based remote BAST sign-off, SLA view.
--
-- Round-2 scope:
--   * Reschedule: the wo_reschedules table already exists (M5 r1) so this
--     migration just adds a `sla_due_at` column on work_orders to drive
--     the SLA-breach view, plus a permission grant for the reschedule
--     action (the column was implicitly governed by field.wo.update in r1
--     but a dedicated permission makes the audit clearer).
--
--   * OTP: bast_records gets `otp_code` (hashed) + `otp_verified_at` so
--     remote sign-off has an integrity tag.
--
--   * SLA: each WO carries `sla_due_at` (set at routing); the SLA view
--     simply returns WOs where status NOT IN (completed,cancelled) AND
--     sla_due_at < NOW(). Auto-pair on breach is deferred to r3.
--
-- Deferred to round-3:
--   * Flutter mobile app
--   * HRIS availability sync
--   * Auto-pair on SLA breach (the watcher job)
--   * Photo upload to object storage

-- =====================================================================
-- 1. work_orders: SLA tracking
-- =====================================================================
ALTER TABLE field.work_orders
    ADD COLUMN IF NOT EXISTS sla_due_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_wo_sla_open
    ON field.work_orders (sla_due_at)
    WHERE status NOT IN ('completed', 'cancelled') AND sla_due_at IS NOT NULL;

COMMENT ON COLUMN field.work_orders.sla_due_at IS
    'Computed at routing/assign time as scheduled_date + product SLA window. '
    'NULL = no SLA (e.g. maintenance WOs without contract). '
    'The SLA-breach view scans for sla_due_at < NOW() on open WOs.';

-- =====================================================================
-- 2. bast_records: OTP for remote sign-off
--
-- otp_code stores a bcrypt-style hash of the 6-digit code we send to
-- the customer over WhatsApp/SMS (round-3 delivery integration; for r2
-- we just record the hash + verified_at when the customer enters it).
-- =====================================================================
ALTER TABLE field.bast_records
    ADD COLUMN IF NOT EXISTS otp_code TEXT,
    ADD COLUMN IF NOT EXISTS otp_verified_at TIMESTAMPTZ;

-- =====================================================================
-- 3. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('field', 'wo.reschedule', 'Reschedule a work order (records audit row)'),
    ('field', 'sla.read',      'View the SLA-breach queue across work orders')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'field' AND p.action IN ('wo.reschedule','sla.read')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_leader'
  AND p.module = 'field' AND p.action IN ('wo.reschedule','sla.read')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'field' AND p.action = 'sla.read'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'field' AND p.action = 'wo.reschedule'
ON CONFLICT DO NOTHING;
