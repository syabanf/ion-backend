-- 0044 — close every non-integration gap from docs/backlog.md.
--
-- Tables / columns this migration adds:
--   1. platform.rate_limit_log  — sliding-window counter for public endpoints
--   2. crm.sales_routing_config — round-robin auto-assignment for self-order
--   3. enterprise.ewo_completion_log — feeds vendor_metrics derivation
--   4. opname.dashboard_snapshots — cached opname roll-ups
--
-- It also extends an existing constraint (kind set on crm.lead_events) so
-- the broader auto-write coverage can use semantic kinds.

BEGIN;

-- ============================================================
-- 1. Rate-limit log — sliding-window key/value for public endpoints
-- ============================================================
-- We don't want a Redis dep for this — Postgres is fast enough for
-- the rate we expect on public endpoints. Each row records one call;
-- the middleware deletes rows older than the window. Keep the table
-- skinny so vacuum stays cheap.

CREATE TABLE IF NOT EXISTS platform.rate_limit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bucket      TEXT NOT NULL,       -- e.g. "ip:1.2.3.4|coverage-check"
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rate_limit_bucket_time
    ON platform.rate_limit_log (bucket, occurred_at DESC);

-- ============================================================
-- 2. Sales routing config — round-robin pointer per branch
-- ============================================================
-- A single-row-per-branch pointer table the self-order handler reads
-- and increments inside a tx. Trivial implementation; if we outgrow
-- it we can swap in a weighted/skill-based router later.

CREATE TABLE IF NOT EXISTS crm.sales_routing_config (
    branch_id        UUID PRIMARY KEY,
    last_sales_id    UUID,        -- the rep we last assigned to
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================
-- 3. EWO completion log — vendor metrics derivation source
-- ============================================================
-- Every time an EWO flips to completed we drop a row here. The
-- derivation cron rolls it into enterprise.vendor_metrics. We log
-- both promised (planned) and actual finish so on-time-% can be
-- computed without joining 5 tables every cron tick.

CREATE TABLE IF NOT EXISTS enterprise.ewo_completion_log (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ewo_id           UUID NOT NULL,
    vendor_id        UUID,          -- nullable: not every EWO has a vendor
    planned_finish   TIMESTAMPTZ,
    actual_finish    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    defect_count     INT NOT NULL DEFAULT 0,
    response_hours   NUMERIC(10,2),
    derived_into_metrics_at TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ewo_complete_undone
    ON enterprise.ewo_completion_log (vendor_id, actual_finish DESC)
    WHERE derived_into_metrics_at IS NULL;

-- Seed: backfill from any EWOs already in completed state so the
-- derivation has something to chew on at first run. We don't have a
-- promised-finish column on enterprise.ewos in this schema, so we
-- leave it null — the derivation job treats null planned_finish as
-- "no SLA target" and excludes it from on-time-% denominator.
INSERT INTO enterprise.ewo_completion_log
    (ewo_id, vendor_id, planned_finish, actual_finish)
SELECT
    e.id,
    NULL,
    NULL,
    COALESCE(e.completed_at, NOW())
FROM enterprise.ewos e
WHERE e.status = 'completed'
ON CONFLICT DO NOTHING;

-- ============================================================
-- 4. Opname snapshots — cached cross-warehouse counts
-- ============================================================

CREATE SCHEMA IF NOT EXISTS opname;

CREATE TABLE IF NOT EXISTS opname.dashboard_snapshots (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    snapshot_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    period_label   TEXT NOT NULL,             -- e.g. "2026-05"
    payload        JSONB NOT NULL,            -- per-warehouse roll-up
    created_by     UUID
);

CREATE INDEX IF NOT EXISTS idx_opname_snap_time
    ON opname.dashboard_snapshots (snapshot_at DESC);

-- ============================================================
-- 5. lead_events kinds — extend the well-known kinds for the broader
--    auto-write coverage. We don't have a CHECK on `kind` so this is
--    purely documentation, captured as a comment.
-- ============================================================

COMMENT ON COLUMN crm.lead_events.kind IS
    'Event kind. Known values: created | status_change | doc_uploaded | '
    'note | sales_reassigned | branch_reassigned | accept_excess_changed | '
    'product_changed | coverage_checked | converted';

-- ============================================================
-- 6. Permissions for the new admin pages
-- ============================================================

INSERT INTO identity.permissions (module, action, description) VALUES
    ('platform','webhook_delivery.read','Read inbound webhook deliveries (forensics)'),
    ('warehouse','opname.read.rollup','Read the cross-warehouse opname dashboard')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND (p.module || '.' || p.action) IN (
    'platform.webhook_delivery.read',
    'warehouse.opname.read.rollup'
  )
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('warehouse_manager','warehouse_staff','operations_admin')
  AND (p.module || '.' || p.action) = 'warehouse.opname.read.rollup'
ON CONFLICT DO NOTHING;

COMMIT;
