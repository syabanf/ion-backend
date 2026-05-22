-- 0048 — Phase 1A closure.
--
-- Closes the gaps surfaced by the ION-Core-Master-PRD-and-Test-Catalog
-- vs the post-Wave-64 build:
--
--   G1 — Operations module (full):
--        * operations schema + bulk_operations + internal_announcements
--        * field.maintenance_events war-room escalation columns
--   G2 — RADIUS schema-driven config:
--        * network.radius_accounts.temp_expires_at column (read from
--          product.temp_activation_window_hours at WO creation)
--   G3 — WO routing geospatial + escalation:
--        * identity.branches per-branch SLA columns (assignment / dispatch / install)
--
-- Every change is additive (no column drops, no constraint changes). Down
-- migration removes only what this file added.

BEGIN;

-- =====================================================================
-- G2 — RADIUS temporary-window expiry
--
-- PRD §13 says the TEMPORARY state is bounded by the product's
-- `temp_activation_window_hours` (default 72h). Wave 47 already seeds
-- the column on crm.products; Wave 65 finally wires it into RADIUS.
--
-- temp_expires_at is set when Provision() runs from WO creation:
--   temp_expires_at = now() + product.temp_activation_window_hours
--
-- Subsequent PromoteToPermanent does NOT clear it — keeping the audit
-- timestamp lets ops see the (never-fired) deadline. The janitor sweep
-- that auto-deactivates expired TEMPORARY rows reads this column.
-- =====================================================================

ALTER TABLE network.radius_accounts
    ADD COLUMN IF NOT EXISTS temp_expires_at TIMESTAMPTZ;

-- Partial index for the janitor sweep: only rows still in TEMPORARY
-- with a deadline matter. Keeps the index small on the long tail of
-- PERMANENT_ACTIVE rows that dominate steady-state.
CREATE INDEX IF NOT EXISTS idx_radius_temp_expiry
    ON network.radius_accounts(temp_expires_at)
    WHERE status = 'temporary' AND temp_expires_at IS NOT NULL;

-- =====================================================================
-- G3.3 — Per-branch SLA config
--
-- PRD §3 (Branch Hierarchy) + §9 (Technician & Field) require SLAs to
-- be configured per branch and inherit Sub Area → Area → Regional when
-- a child branch leaves them NULL. Wave 65 stores the columns; the
-- field service reads with COALESCE down the parent chain.
--
-- All durations stored in minutes for precision. NULL = inherit from
-- parent; NULL all the way to regional = use platform default.
-- =====================================================================

ALTER TABLE identity.branches
    ADD COLUMN IF NOT EXISTS sla_assignment_minutes INTEGER,
    ADD COLUMN IF NOT EXISTS sla_dispatch_minutes   INTEGER,
    ADD COLUMN IF NOT EXISTS sla_install_minutes    INTEGER;

-- =====================================================================
-- G1.1 — Operations module schema
--
-- New `operations` schema houses the cross-cutting ops surfaces. We
-- keep maintenance_events under `field.` (where it lives today) for
-- backward compat and add a war-room escalation hook there.
-- =====================================================================

CREATE SCHEMA IF NOT EXISTS operations;

-- ----- bulk_operations: bulk plan change, ODP migration, WO creation
--
-- Lifecycle: draft → previewed → approved → executing → completed (or rejected)
-- Preview computes the impact set before approval; execution writes the
-- per-row changes to a journal column for rollback / audit.
CREATE TABLE IF NOT EXISTS operations.bulk_operations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_code  TEXT NOT NULL UNIQUE,
    -- Operation type drives both the preview computation and the
    -- execution path. Keeping the catalog narrow in this wave.
    op_kind         TEXT NOT NULL CHECK (op_kind IN (
        'plan_change','odp_migration','wo_create'
    )),
    title           TEXT NOT NULL,
    description     TEXT,
    -- payload: arbitrary input parameters, e.g. for plan_change:
    --   {"customer_ids":[...], "to_product_id":"...", "effective_date":"..."}
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- preview_summary: counts + sample rows the dashboard renders
    --   {"affected_count":42, "sample":[{...}], "billing_delta_idr":12345}
    preview_summary JSONB,
    -- execution_journal: per-row outcome rows captured during execute()
    --   [{"customer_id":"...", "status":"ok"|"failed", "error":"..."}]
    execution_journal JSONB,
    -- branch_id: filter scope (NULL = system-wide); the operator's branch
    -- defaults this when null in payload.
    branch_id       UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'draft' CHECK (status IN (
        'draft','previewed','approved','executing','completed','rejected'
    )),
    created_by      UUID,
    approved_by     UUID,
    approved_at     TIMESTAMPTZ,
    executed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bulk_ops_status
    ON operations.bulk_operations(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bulk_ops_branch
    ON operations.bulk_operations(branch_id)
    WHERE branch_id IS NOT NULL;

-- ----- internal_announcements: staff broadcasts (NOT customer-facing)
--
-- Targeting is JSONB so we can grow it (roles, branches, employee_ids,
-- skill tags...) without schema changes. The notifyx dispatcher reads
-- this on a periodic tick and fans out to the chosen channels.
CREATE TABLE IF NOT EXISTS operations.internal_announcements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title           TEXT NOT NULL,
    body            TEXT NOT NULL,
    severity        TEXT NOT NULL DEFAULT 'info' CHECK (severity IN (
        'info','warning','critical'
    )),
    -- targeting: {"roles":["technician"], "branches":["uuid",...]}
    -- empty {} = everyone.
    targeting       JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- channels: which delivery mechanisms to use. ["push","email","wa"]
    channels        JSONB NOT NULL DEFAULT '["push"]'::jsonb,
    -- scheduled_at NULL = send immediately on next dispatcher tick.
    scheduled_at    TIMESTAMPTZ,
    sent_at         TIMESTAMPTZ,
    sent_count      INTEGER NOT NULL DEFAULT 0,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_announcements_pending
    ON operations.internal_announcements(scheduled_at)
    WHERE sent_at IS NULL;

-- ----- maintenance_events war-room escalation hook
--
-- PRD §14 (Operations) requires maintenance events that overrun or
-- cause unexpected impact to escalate into the War Room. The War Room
-- UI is Phase 1C/future, but the schema-ready hook is Phase 1A scope.
ALTER TABLE field.maintenance_events
    ADD COLUMN IF NOT EXISTS escalated_to_war_room_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS war_room_incident_id     UUID,
    ADD COLUMN IF NOT EXISTS escalation_reason        TEXT;

-- =====================================================================
-- Operations permissions — extend the RBAC catalog
-- =====================================================================

INSERT INTO identity.permissions (module, action, description) VALUES
    ('operations', 'bulk.read',          'View bulk operations queue'),
    ('operations', 'bulk.create',        'Draft a new bulk operation'),
    ('operations', 'bulk.preview',       'Compute the impact set for a bulk op'),
    ('operations', 'bulk.approve',       'Approve a previewed bulk op for execution'),
    ('operations', 'bulk.execute',       'Execute an approved bulk op'),
    ('operations', 'announcement.read',  'View internal announcements'),
    ('operations', 'announcement.create','Broadcast an internal announcement'),
    ('operations', 'calendar.read',      'View the operational calendar'),
    ('operations', 'sla.read',           'View the cross-module SLA dashboard'),
    ('operations', 'escalate',           'Escalate maintenance into the War Room')
ON CONFLICT (module, action) DO NOTHING;

-- Grant the new permissions to operations_admin + super_admin so the
-- surfaces are usable on day one. Other roles can be granted via the
-- admin UI. The 0001 seed used `operations_admin` (not `ops_admin`).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
  FROM identity.roles r
  CROSS JOIN identity.permissions p
 WHERE r.name IN ('operations_admin','super_admin')
   AND p.module = 'operations'
ON CONFLICT (role_id, permission_id) DO NOTHING;

COMMIT;
