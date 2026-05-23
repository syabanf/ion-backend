-- Wave 96 — Dual EWO + TL Scheduling.
--
-- Per the Wave 91 audit (EWO Dual + TL Scheduling sections), this wave
-- splits the single-row `enterprise.ewos` table into two sides:
--
--   EWO-X — commercial owner side (the customer-facing subsidiary
--           tracks commercial scope + invoicing posture).
--   EWO-Y — executing sister side (the actual field-execution slice,
--           auto-spawned when its parent IC-PO is accepted).
--
-- EWO-X already exists (every row pre-Wave 96 is the commercial side).
-- The backfill below stamps `side='x'` on every legacy row so the
-- check constraint is satisfied without a pre-migration data fix.
--
-- Scheduling fields land on the same table — keeping EWO-X / EWO-Y
-- joined to schedule simplifies the TL dashboard query path (one
-- row per work order, side discriminator covers the dual semantics).
-- `schedule_locked` flips true when the EWO transitions to
-- in_progress (TC-TL-009: reschedule blocked once work has begun).

BEGIN;

-- =====================================================================
-- enterprise.ewos — additive column set + check constraints + backfill
-- =====================================================================

ALTER TABLE enterprise.ewos
    ADD COLUMN side TEXT NOT NULL DEFAULT 'x'
        CHECK (side IN ('x', 'y')),
    ADD COLUMN executing_subsidiary_id UUID
        REFERENCES enterprise.subsidiaries(id) ON DELETE RESTRICT,
    ADD COLUMN intercompany_po_id UUID
        REFERENCES enterprise.intercompany_pos(id) ON DELETE RESTRICT,
    ADD COLUMN paired_ewo_id UUID
        REFERENCES enterprise.ewos(id) ON DELETE SET NULL,
    ADD COLUMN scheduled_start_date TIMESTAMPTZ,
    ADD COLUMN scheduled_end_date TIMESTAMPTZ,
    ADD COLUMN duration_days INTEGER,
    ADD COLUMN assigned_technician_user_id UUID,
    ADD COLUMN assigned_team_lead_user_id UUID,
    ADD COLUMN schedule_locked BOOLEAN NOT NULL DEFAULT FALSE;

-- Explicit backfill for pre-existing rows. The DEFAULT 'x' already
-- covers new inserts during the migration; this is belt-and-suspenders
-- for environments where a column-default isn't applied to existing
-- rows (some pg flavors require an explicit update on add).
UPDATE enterprise.ewos
SET side = 'x'
WHERE side IS NULL OR side = '';

-- =====================================================================
-- Indexes — TL dashboard query (assignment + date range) + side filter
-- =====================================================================

CREATE INDEX IF NOT EXISTS idx_ewos_executing_side_status
    ON enterprise.ewos (executing_subsidiary_id, side, status);

CREATE INDEX IF NOT EXISTS idx_ewos_paired
    ON enterprise.ewos (paired_ewo_id);

CREATE INDEX IF NOT EXISTS idx_ewos_team_lead_schedule
    ON enterprise.ewos (assigned_team_lead_user_id, scheduled_start_date);

CREATE INDEX IF NOT EXISTS idx_ewos_technician_schedule
    ON enterprise.ewos (assigned_technician_user_id, scheduled_start_date);

-- =====================================================================
-- enterprise.ewo_schedule_history — append-only audit trail for
-- reschedules. One row written for each Reschedule call capturing the
-- pre-change values; the EWO row itself carries the current values.
-- =====================================================================
CREATE TABLE enterprise.ewo_schedule_history (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ewo_id          UUID NOT NULL
        REFERENCES enterprise.ewos(id) ON DELETE CASCADE,
    prev_start      TIMESTAMPTZ,
    prev_end        TIMESTAMPTZ,
    prev_team_lead  UUID,
    prev_technician UUID,
    changed_by      UUID NOT NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason          TEXT
);

CREATE INDEX idx_ewo_schedule_history_ewo
    ON enterprise.ewo_schedule_history (ewo_id, changed_at DESC);

-- =====================================================================
-- RBAC — team_lead role (Wave 96 introduces) + new permissions
-- =====================================================================

-- Add the team_lead role if it doesn't already exist. Broadband uses
-- `team_leader` (with the 'er' suffix) — `team_lead` is a separate
-- enterprise-side role per the Wave 96 spec.
INSERT INTO identity.roles (name, description) VALUES
    ('team_lead', 'Enterprise EWO team lead — schedules + assigns technicians on EWOs')
ON CONFLICT (name) DO NOTHING;

INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'tl_scheduling.read',  'View scheduled EWOs + schedule history'),
    ('enterprise', 'tl_scheduling.write', 'Schedule / reschedule / start EWOs'),
    ('enterprise', 'ewo.dual.read',       'View EWO-X / EWO-Y pair across subsidiaries')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin gets every new permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN ('tl_scheduling.read', 'tl_scheduling.write', 'ewo.dual.read')
ON CONFLICT DO NOTHING;

-- team_lead gets the TL scheduling surface.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_lead'
  AND p.module = 'enterprise'
  AND p.action IN ('tl_scheduling.read', 'tl_scheduling.write')
ON CONFLICT DO NOTHING;

-- operations_admin can see the dual-EWO pair (executing-side visibility).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'enterprise'
  AND p.action IN ('ewo.dual.read', 'tl_scheduling.read')
ON CONFLICT DO NOTHING;

COMMIT;
