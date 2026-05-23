-- Wave 126 — Maintenance enhancements + Internal Announcements +
-- Operational Calendar + Cross-Module SLA Ops View + CS Dashboards.
--
-- Targets ~36 TCs across:
--   - Planned Maintenance (TC-PM-001..010)
--   - Maintenance Escalation (TC-MES-001..004)
--   - Internal Announcements (TC-ANN-001..005)
--   - Operational Calendar (TC-OPC-001..006)
--   - Cross-Module SLA Ops View (TC-CSM-001..004)
--   - CS Dashboards (TC-CSD-001..006)
--
-- Gaps from the Wave 122 audit:
--   - Maintenance: no affected_customer materialization, no >100-customer
--     joint-approval threshold, no notification-lead-time cron (24h
--     broadband / 72h enterprise), no overrun-detect cron.
--   - Internal Announcements: dispatcher absent; severity enum mismatch
--     (DB info|warning|critical vs PRD info|important|urgent); no
--     recipient table for read receipts.
--   - CS Dashboards: backend aggregation routes don't exist; agent
--     queue + supervisor team SLA + escalation queue all computed
--     client-side from flat lists today.

BEGIN;

-- =====================================================================
-- 0. schemas (defensive — operations/cs exist from earlier waves)
-- =====================================================================
CREATE SCHEMA IF NOT EXISTS operations;
CREATE SCHEMA IF NOT EXISTS cs;

-- =====================================================================
-- 1. field.maintenance_events — extend with notification + approval cols
-- =====================================================================
ALTER TABLE field.maintenance_events
    ADD COLUMN IF NOT EXISTS lead_time_notify_hours INT NOT NULL DEFAULT 24,
    ADD COLUMN IF NOT EXISTS customer_segment       TEXT NOT NULL DEFAULT 'broadband',
    ADD COLUMN IF NOT EXISTS approval_required      BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS approved_by            UUID,
    ADD COLUMN IF NOT EXISTS approved_at            TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS overrun_at             TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS overrun_notified       BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS affected_customer_count INT NOT NULL DEFAULT 0;

ALTER TABLE field.maintenance_events
    DROP CONSTRAINT IF EXISTS maintenance_events_customer_segment_check;
ALTER TABLE field.maintenance_events
    ADD CONSTRAINT maintenance_events_customer_segment_check
    CHECK (customer_segment IN ('broadband','enterprise','mixed'));

CREATE INDEX IF NOT EXISTS idx_maint_events_overrun
    ON field.maintenance_events (overrun_at)
    WHERE overrun_at IS NOT NULL AND overrun_notified = FALSE;

CREATE INDEX IF NOT EXISTS idx_maint_events_approval
    ON field.maintenance_events (approval_required, approved_at)
    WHERE approval_required = TRUE AND approved_at IS NULL;

-- =====================================================================
-- 2. operations.maintenance_affected_customers
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.maintenance_affected_customers (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    maintenance_event_id UUID NOT NULL REFERENCES field.maintenance_events(id) ON DELETE CASCADE,
    customer_id          UUID NOT NULL,
    customer_segment     TEXT NOT NULL DEFAULT 'broadband',
    notified_at          TIMESTAMPTZ,
    notification_channel TEXT,
    error_msg            TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (maintenance_event_id, customer_id)
);

CREATE INDEX IF NOT EXISTS idx_maint_affected_customer
    ON operations.maintenance_affected_customers (customer_id, notified_at DESC);

CREATE INDEX IF NOT EXISTS idx_maint_affected_pending
    ON operations.maintenance_affected_customers (maintenance_event_id)
    WHERE notified_at IS NULL;

-- =====================================================================
-- 3. operations.maintenance_escalations
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.maintenance_escalations (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    maintenance_event_id UUID NOT NULL REFERENCES field.maintenance_events(id) ON DELETE CASCADE,
    level                INT NOT NULL CHECK (level BETWEEN 1 AND 4),
    reason               TEXT,
    escalated_to_user_id UUID,
    escalated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acknowledged_at      TIMESTAMPTZ,
    resolved_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_maint_escalations_event
    ON operations.maintenance_escalations (maintenance_event_id, level);

CREATE INDEX IF NOT EXISTS idx_maint_escalations_assignee
    ON operations.maintenance_escalations (escalated_to_user_id, resolved_at);

-- =====================================================================
-- 4. operations.internal_announcements — extend + fix severity enum
-- =====================================================================
ALTER TABLE operations.internal_announcements
    DROP CONSTRAINT IF EXISTS internal_announcements_severity_check;

-- Backfill old severity values to the PRD-correct set:
--   warning  → important
--   critical → urgent
--   else     → info (already valid)
UPDATE operations.internal_announcements
   SET severity = CASE
       WHEN severity = 'warning'  THEN 'important'
       WHEN severity = 'critical' THEN 'urgent'
       WHEN severity NOT IN ('info','important','urgent') THEN 'info'
       ELSE severity
   END
 WHERE severity IS NOT NULL;

ALTER TABLE operations.internal_announcements
    ADD CONSTRAINT internal_announcements_severity_check
    CHECK (severity IN ('info','important','urgent'));

ALTER TABLE operations.internal_announcements
    ADD COLUMN IF NOT EXISTS target_audience  TEXT NOT NULL DEFAULT 'all',
    ADD COLUMN IF NOT EXISTS expires_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dispatched_at    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dispatch_status  TEXT NOT NULL DEFAULT 'pending';

ALTER TABLE operations.internal_announcements
    DROP CONSTRAINT IF EXISTS internal_announcements_target_audience_check;
ALTER TABLE operations.internal_announcements
    ADD CONSTRAINT internal_announcements_target_audience_check
    CHECK (target_audience IN ('all','agents','supervisors','technicians','customers'));

ALTER TABLE operations.internal_announcements
    DROP CONSTRAINT IF EXISTS internal_announcements_dispatch_status_check;
ALTER TABLE operations.internal_announcements
    ADD CONSTRAINT internal_announcements_dispatch_status_check
    CHECK (dispatch_status IN ('pending','dispatching','dispatched','failed','partial'));

CREATE INDEX IF NOT EXISTS idx_announcements_dispatch
    ON operations.internal_announcements (dispatch_status, scheduled_at)
    WHERE dispatch_status IN ('pending','dispatching');

-- =====================================================================
-- 5. operations.announcement_recipients
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.announcement_recipients (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    announcement_id UUID NOT NULL REFERENCES operations.internal_announcements(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL,
    delivered_at    TIMESTAMPTZ,
    read_at         TIMESTAMPTZ,
    channel         TEXT,
    error_msg       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (announcement_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_announcement_recipients_user
    ON operations.announcement_recipients (user_id, read_at, delivered_at DESC);

-- =====================================================================
-- 6. operations.calendar_events — unified events feed
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.calendar_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_kind   TEXT NOT NULL CHECK (event_kind IN (
        'maintenance','holiday','training','blackout','announcement',
        'release','contract_renewal','sla_deadline','custom'
    )),
    event_source TEXT NOT NULL CHECK (event_source IN (
        'field.maintenance','operations.bulk_jobs','operations.announcement',
        'enterprise.invoice_plan','custom'
    )),
    source_id    UUID,
    title        TEXT NOT NULL,
    description  TEXT,
    scope        TEXT NOT NULL DEFAULT 'global' CHECK (scope IN (
        'global','branch','team','user'
    )),
    scope_id     UUID,
    all_day      BOOLEAN NOT NULL DEFAULT FALSE,
    starts_at    TIMESTAMPTZ NOT NULL,
    ends_at      TIMESTAMPTZ,
    color_hex    TEXT,
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_calendar_range
    ON operations.calendar_events (starts_at, ends_at);
CREATE INDEX IF NOT EXISTS idx_calendar_kind
    ON operations.calendar_events (event_kind, starts_at);
CREATE INDEX IF NOT EXISTS idx_calendar_scope
    ON operations.calendar_events (scope, scope_id, starts_at);
-- Auto-sync idempotency: a single (source, source_id) maps to one row.
CREATE UNIQUE INDEX IF NOT EXISTS uq_calendar_source
    ON operations.calendar_events (event_source, source_id)
    WHERE source_id IS NOT NULL;

-- =====================================================================
-- 7. operations.cross_module_sla_snapshots
-- =====================================================================
CREATE TABLE IF NOT EXISTS operations.cross_module_sla_snapshots (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    module                TEXT NOT NULL CHECK (module IN (
        'cs','field','enterprise','billing','warehouse','nocmon'
    )),
    aggregated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    period_start          TIMESTAMPTZ,
    period_end            TIMESTAMPTZ,
    total_at_risk         INT NOT NULL DEFAULT 0,
    total_breached        INT NOT NULL DEFAULT 0,
    p50_remaining_minutes INT NOT NULL DEFAULT 0,
    p95_remaining_minutes INT NOT NULL DEFAULT 0,
    top_breachers         JSONB NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_xmod_sla_module_at
    ON operations.cross_module_sla_snapshots (module, aggregated_at DESC);

-- =====================================================================
-- 8. cs.dashboard_aggregations — precomputed dashboard payloads
-- =====================================================================
CREATE TABLE IF NOT EXISTS cs.dashboard_aggregations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            TEXT NOT NULL CHECK (kind IN (
        'agent_queue','supervisor_team_sla','escalation_queue',
        'satisfaction_summary','channel_distribution'
    )),
    scope_user_id   UUID,
    scope_team_id   UUID,
    aggregated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    period_start    TIMESTAMPTZ,
    period_end      TIMESTAMPTZ,
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_cs_dash_kind
    ON cs.dashboard_aggregations (kind, aggregated_at DESC);
CREATE INDEX IF NOT EXISTS idx_cs_dash_user
    ON cs.dashboard_aggregations (scope_user_id, aggregated_at DESC)
    WHERE scope_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cs_dash_team
    ON cs.dashboard_aggregations (scope_team_id, aggregated_at DESC)
    WHERE scope_team_id IS NOT NULL;

-- =====================================================================
-- 9. Permissions — Wave 126 surface
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('operations.maintenance', 'approve',       'Approve a planned maintenance event'),
    ('operations.maintenance', 'escalate',      'Escalate a maintenance event to the next level'),
    ('operations.announcement','dispatch',      'Dispatch a pending announcement'),
    ('operations.calendar',    'read',          'Read the unified operational calendar'),
    ('operations.calendar',    'write',         'Create/update calendar entries'),
    ('ops.sla',                'cross_module_view.read', 'View the cross-module SLA dashboard'),
    ('cs.dashboard',           'agent_queue.read',       'Read CS agent queue dashboard'),
    ('cs.dashboard',           'team_sla.read',          'Read CS supervisor team SLA dashboard'),
    ('cs.dashboard',           'escalation.read',        'Read CS escalation queue dashboard')
ON CONFLICT (module, action) DO NOTHING;

-- Seed the new permissions to operations_admin + super_admin + cs_supervisor.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
  FROM identity.roles r
  CROSS JOIN identity.permissions p
 WHERE (
     (r.name IN ('operations_admin','super_admin') AND p.module IN (
         'operations.maintenance','operations.announcement',
         'operations.calendar','ops.sla'
     )) OR
     (r.name IN ('cs_supervisor','super_admin') AND p.module = 'cs.dashboard') OR
     (r.name IN ('cs_agent') AND p.module = 'cs.dashboard' AND p.action = 'agent_queue.read')
 )
ON CONFLICT (role_id, permission_id) DO NOTHING;

COMMIT;
