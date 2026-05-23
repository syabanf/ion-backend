-- Wave 125 — Bulk Ops Executors (Phase 1C Broadband).
--
-- Targets 18 TCs across Bulk Plan Change (8), Bulk ODP Migration (6), and
-- Bulk WO Creation (4). The Wave 71 `operations.bulk_operations` table +
-- the handler in internal/operations/adapter/http/handler.go landed the
-- framework with stub executors (status flips, no per-row work).
--
-- This migration adds the per-kind item tables + a new `operations.bulk_jobs`
-- aggregate that's wired into the new executor service. `bulk_operations`
-- (Wave 71) stays intact — the legacy preview/approve surface continues
-- to serve the existing UI; the new executor service runs alongside.
--
-- Cross-context bridges (crm.plan_change_requests / network.ports /
-- field.work_orders) are SQL-only — no Go imports across bounded
-- contexts.

BEGIN;

-- =====================================================================
-- 0. schema (defensive — created by 0048, but IF NOT EXISTS protects
--    a fresh DB that runs migrations out of order during testing)
-- =====================================================================
CREATE SCHEMA IF NOT EXISTS operations;

-- =====================================================================
-- 1. operations.bulk_jobs — the new executor-aware aggregate
--
-- Distinct from `operations.bulk_operations` (Wave 71):
--   - `bulk_operations` = legacy preview/approve workflow (kept for
--     back-compat; the existing UI + handler still drive it)
--   - `bulk_jobs`       = executor framework with CSV-imported items,
--     8-state status SM, dry-run flag, idempotent runner
--
-- A row here is created via POST /api/operations/bulk/{kind} (multipart
-- CSV upload) and progressed via the BulkExecutorService.
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.bulk_jobs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind                TEXT NOT NULL CHECK (kind IN (
        'plan_change','odp_migration','wo_creation',
        'wo_cancellation','customer_segment_export'
    )),
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending','validating','running','completed',
        'failed','partial','cancelled'
    )),
    total_items         INT NOT NULL DEFAULT 0,
    processed_items     INT NOT NULL DEFAULT 0,
    succeeded_items     INT NOT NULL DEFAULT 0,
    failed_items        INT NOT NULL DEFAULT 0,
    skipped_items       INT NOT NULL DEFAULT 0,
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    error_summary       JSONB,
    dry_run             BOOLEAN NOT NULL DEFAULT FALSE,
    created_by          UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bulk_jobs_status
    ON operations.bulk_jobs (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bulk_jobs_kind
    ON operations.bulk_jobs (kind, created_at DESC);

-- =====================================================================
-- 2. operations.bulk_plan_change_items
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.bulk_plan_change_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bulk_job_id     UUID NOT NULL REFERENCES operations.bulk_jobs(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL,
    current_plan_id UUID,
    target_plan_id  UUID NOT NULL,
    effective_at    TIMESTAMPTZ,
    status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued','validating','validated','processing',
        'succeeded','failed','skipped'
    )),
    error_msg       TEXT,
    processed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bpc_items_job_status
    ON operations.bulk_plan_change_items (bulk_job_id, status);
CREATE INDEX IF NOT EXISTS idx_bpc_items_customer
    ON operations.bulk_plan_change_items (customer_id);

-- =====================================================================
-- 3. operations.bulk_odp_migration_items
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.bulk_odp_migration_items (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bulk_job_id             UUID NOT NULL REFERENCES operations.bulk_jobs(id) ON DELETE CASCADE,
    customer_id             UUID NOT NULL,
    from_olt_port_id        UUID,
    to_olt_port_id          UUID NOT NULL,
    scheduled_window_start  TIMESTAMPTZ,
    scheduled_window_end    TIMESTAMPTZ,
    status                  TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued','validating','validated','staged',
        'migrated','failed','rolled_back'
    )),
    wo_id                   UUID,
    error_msg               TEXT,
    processed_at            TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bom_items_job_status
    ON operations.bulk_odp_migration_items (bulk_job_id, status);
CREATE INDEX IF NOT EXISTS idx_bom_items_customer
    ON operations.bulk_odp_migration_items (customer_id);
CREATE INDEX IF NOT EXISTS idx_bom_items_to_port
    ON operations.bulk_odp_migration_items (to_olt_port_id);

-- =====================================================================
-- 4. operations.bulk_wo_creation_items
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.bulk_wo_creation_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bulk_job_id     UUID NOT NULL REFERENCES operations.bulk_jobs(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL,
    wo_template_id  UUID,
    wo_type         TEXT,
    scheduled_at    TIMESTAMPTZ,
    status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued','validating','validated','created',
        'failed','duplicate'
    )),
    created_wo_id   UUID,
    error_msg       TEXT,
    processed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bwo_items_job_status
    ON operations.bulk_wo_creation_items (bulk_job_id, status);
CREATE INDEX IF NOT EXISTS idx_bwo_items_customer
    ON operations.bulk_wo_creation_items (customer_id);

-- =====================================================================
-- 5. Permissions — new per-kind grants on top of the Wave 71 base
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('operations', 'bulk.run',                   'Run a bulk job through the executor'),
    ('operations', 'bulk.cancel',                'Cancel a running bulk job'),
    ('operations', 'bulk_plan_change.run',       'Execute Bulk Plan Change items'),
    ('operations', 'bulk_odp_migration.run',     'Execute Bulk ODP Migration items'),
    ('operations', 'bulk_wo_creation.run',       'Execute Bulk WO Creation items')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin + operations_admin get everything.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
  FROM identity.roles r
  CROSS JOIN identity.permissions p
 WHERE r.name IN ('super_admin','operations_admin')
   AND p.module = 'operations'
   AND p.action IN (
       'bulk.run','bulk.cancel',
       'bulk_plan_change.run','bulk_odp_migration.run','bulk_wo_creation.run'
   )
ON CONFLICT (role_id, permission_id) DO NOTHING;

-- ops_lead can read + run but cannot cancel running jobs (cancel is
-- a coordination action and lives with operations_admin).
INSERT INTO identity.roles (id, name, description)
VALUES (gen_random_uuid(), 'ops_lead', 'Operations team lead — bulk ops runner')
ON CONFLICT (name) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
  FROM identity.roles r
  CROSS JOIN identity.permissions p
 WHERE r.name = 'ops_lead'
   AND p.module = 'operations'
   AND p.action IN (
       'bulk.read','bulk.run',
       'bulk_plan_change.run','bulk_odp_migration.run','bulk_wo_creation.run'
   )
ON CONFLICT (role_id, permission_id) DO NOTHING;

COMMIT;
