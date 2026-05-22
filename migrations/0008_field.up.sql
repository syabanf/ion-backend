-- M5 Round 1 — Technician & Field skeleton.
--
-- Lifecycle (round 1 scope): create WO → route to team (by branch) →
-- assign technicians (lead/observer pair) → in_progress → submit
-- checklist responses + resolution items → submit BAST → NOC verify
-- (approve/reject). Round 2 adds: HRIS sync, OTP sign-off, mobile app,
-- warehouse dispatch integration, auto-pair on SLA breach.
--
-- Deferred to round 2 (not created here):
--   - field.wo_consumption (warehouse cross-cut)
--   - field.hris_sync_log
--   - field.wo_dispatch (asset QR scans at warehouse)

CREATE SCHEMA IF NOT EXISTS field;

-- =====================================================================
-- 1. Teams — Team Leader + members, scoped to a branch.
--
-- Per PRD: Team Leader assigns technician pairs from their area's roster.
-- We model a team as a branch-scoped roster led by one user. Cross-area
-- dispatch is allowed (a WO routed to one team can be assigned to a
-- technician from another team by a Team Leader+).
-- =====================================================================
CREATE TABLE field.teams (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    branch_id       UUID NOT NULL REFERENCES identity.branches(id) ON DELETE RESTRICT,
    team_leader_id  UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_teams_branch ON field.teams (branch_id);
CREATE INDEX idx_teams_leader ON field.teams (team_leader_id);

CREATE TABLE field.team_members (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID NOT NULL REFERENCES field.teams(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    grade           TEXT NOT NULL CHECK (grade IN ('senior','junior')),
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (team_id, user_id)
);
CREATE INDEX idx_team_members_team ON field.team_members (team_id);
CREATE INDEX idx_team_members_user ON field.team_members (user_id);

-- =====================================================================
-- 2. Work Orders
--
-- The WO is the operational anchor: M5 owns its lifecycle; M3 cross-cuts
-- for materials; M6 cross-cuts for payment gating; M2 cross-cuts for
-- temporary→permanent RADIUS transition on NOC approval.
--
-- Phase 1 only supports `new_installation` + `termination` WO types from
-- the order flow; `maintenance` arrives once tickets exist (Phase 2).
-- =====================================================================
CREATE TABLE field.work_orders (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_number               TEXT NOT NULL UNIQUE,
    -- Loose FKs: the field service must operate even when CRM is split
    -- to its own process. Soft-typed UUID columns + read-by-id contracts.
    order_id                UUID,                       -- crm.orders (when present)
    customer_id             UUID NOT NULL,              -- crm.customers
    wo_type                 TEXT NOT NULL CHECK (wo_type IN ('new_installation','maintenance','termination')),
    product_type            TEXT NOT NULL DEFAULT 'broadband',
    maintenance_subtype     TEXT,
    address                 TEXT NOT NULL,
    branch_id               UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    priority                TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    status                  TEXT NOT NULL DEFAULT 'created' CHECK (status IN (
        'created','unassigned','assigned','dispatched','in_progress',
        'pending_noc_verification','completed','rescheduled','cancelled'
    )),
    scheduled_date          TIMESTAMPTZ,
    team_id                 UUID REFERENCES field.teams(id) ON DELETE SET NULL,
    team_leader_id          UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    is_emergency            BOOLEAN NOT NULL DEFAULT FALSE,
    is_cross_area           BOOLEAN NOT NULL DEFAULT FALSE,
    notes                   TEXT,
    created_by              UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_wo_status      ON field.work_orders (status);
CREATE INDEX idx_wo_customer    ON field.work_orders (customer_id);
CREATE INDEX idx_wo_order       ON field.work_orders (order_id);
CREATE INDEX idx_wo_branch      ON field.work_orders (branch_id);
CREATE INDEX idx_wo_team        ON field.work_orders (team_id);
CREATE INDEX idx_wo_scheduled   ON field.work_orders (scheduled_date);

-- =====================================================================
-- 3. WO Assignments — paired techs per WO (lead/observer).
-- =====================================================================
CREATE TABLE field.wo_assignments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_id           UUID NOT NULL REFERENCES field.work_orders(id) ON DELETE CASCADE,
    technician_id   UUID NOT NULL REFERENCES identity.users(id) ON DELETE RESTRICT,
    grade           TEXT NOT NULL CHECK (grade IN ('senior','junior')),
    wo_role         TEXT NOT NULL DEFAULT 'observer' CHECK (wo_role IN ('lead','observer')),
    assigned_by     UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    assigned_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (wo_id, technician_id)
);
CREATE INDEX idx_wo_assign_wo   ON field.wo_assignments (wo_id);
CREATE INDEX idx_wo_assign_tech ON field.wo_assignments (technician_id);

-- One row per WO can be lead; enforce via partial unique index instead of
-- a check so observer counts stay unrestricted.
CREATE UNIQUE INDEX uniq_wo_lead
    ON field.wo_assignments (wo_id)
    WHERE wo_role = 'lead';

-- =====================================================================
-- 4. Reschedule audit log
-- =====================================================================
CREATE TABLE field.wo_reschedules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_id           UUID NOT NULL REFERENCES field.work_orders(id) ON DELETE CASCADE,
    reason          TEXT NOT NULL CHECK (reason IN (
        'customer_not_available','site_not_ready','equipment_issue','customer_request','other'
    )),
    notes           TEXT,
    original_date   TIMESTAMPTZ,
    new_date        TIMESTAMPTZ,
    rescheduled_by  UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_wo_reschedules_wo ON field.wo_reschedules (wo_id);

-- =====================================================================
-- 5. Checklist templates + items + responses
--
-- A WO loads its checklist from the (wo_type, product_type, maintenance_subtype)
-- template at dispatch time. We don't snapshot template_items onto the WO;
-- if the template changes mid-WO, the checklist UI uses the latest items.
-- Phase 1: change-rate is so low this hasn't hurt anyone. Phase 2 should
-- snapshot if we ever start editing templates while WOs are live.
-- =====================================================================
CREATE TABLE field.wo_checklist_templates (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_type                 TEXT NOT NULL CHECK (wo_type IN ('new_installation','maintenance','termination')),
    product_type            TEXT NOT NULL,
    maintenance_subtype     TEXT,
    min_photos_required     INT NOT NULL DEFAULT 3,
    gps_stamp_on_photos     BOOLEAN NOT NULL DEFAULT TRUE,
    active                  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (wo_type, product_type, maintenance_subtype)
);

CREATE TABLE field.wo_checklist_template_items (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id         UUID NOT NULL REFERENCES field.wo_checklist_templates(id) ON DELETE CASCADE,
    item_order          INT NOT NULL,
    item_type           TEXT NOT NULL CHECK (item_type IN (
        'photo','text','number','checkbox','qr_scan','signature','gps_location'
    )),
    label               TEXT NOT NULL,
    required            BOOLEAN NOT NULL DEFAULT TRUE,
    photo_tag           TEXT,
    gps_required        BOOLEAN NOT NULL DEFAULT FALSE,
    min_accuracy_meters INT,
    UNIQUE (template_id, item_order)
);

CREATE TABLE field.wo_checklist_responses (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_id           UUID NOT NULL REFERENCES field.work_orders(id) ON DELETE CASCADE,
    template_item_id UUID NOT NULL REFERENCES field.wo_checklist_template_items(id),
    response_text   TEXT,
    file_url        TEXT,
    gps_lat         DOUBLE PRECISION,
    gps_lng         DOUBLE PRECISION,
    gps_accuracy_m  DOUBLE PRECISION,
    submitted_by    UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (wo_id, template_item_id)
);
CREATE INDEX idx_wo_responses_wo ON field.wo_checklist_responses (wo_id);

-- =====================================================================
-- 6. Resolution items — free-form on-site work log
-- =====================================================================
CREATE TABLE field.wo_resolution_items (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_id               UUID NOT NULL REFERENCES field.work_orders(id) ON DELETE CASCADE,
    item_order          INT NOT NULL,
    item_label          TEXT NOT NULL,
    category            TEXT CHECK (category IN ('config','hardware','cabling','signal','software','other')),
    finding             TEXT,
    action_taken        TEXT,
    resolution_status   TEXT NOT NULL CHECK (resolution_status IN (
        'resolved','partial','unable','escalated_to_noc','escalated_to_team_leader'
    )),
    time_spent_minutes  INT,
    resolved_by         UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    logged_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_wo_resolution_wo ON field.wo_resolution_items (wo_id);

-- =====================================================================
-- 7. BAST records — immutable on submit; rejection creates a NEW row.
--
-- Per PRD: payment-gate-NOC rule means a BAST sits at noc_status='pending'
-- until M6 marks the linked OTC invoice paid; only then does NOC pick it
-- up to approve/reject. In M5 round 1 we model the column, but the
-- payment-gate enforcement plumbs through later when invoices exist.
-- =====================================================================
CREATE TABLE field.bast_records (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_id               UUID NOT NULL REFERENCES field.work_orders(id) ON DELETE CASCADE,
    customer_id         UUID NOT NULL,
    compiled_data       JSONB NOT NULL,
    sign_off_mode       TEXT NOT NULL CHECK (sign_off_mode IN ('on_site','remote')),
    customer_sig_url    TEXT,
    otp_used            BOOLEAN NOT NULL DEFAULT FALSE,
    sign_off_at         TIMESTAMPTZ NOT NULL,
    sign_off_gps_lat    DOUBLE PRECISION,
    sign_off_gps_lng    DOUBLE PRECISION,
    submitted_by        UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    submitted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    noc_status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (noc_status IN ('pending','approved','rejected')),
    noc_verified_by     UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    noc_verified_at     TIMESTAMPTZ,
    noc_notes           TEXT
);
CREATE INDEX idx_bast_wo       ON field.bast_records (wo_id);
CREATE INDEX idx_bast_customer ON field.bast_records (customer_id);
CREATE INDEX idx_bast_noc      ON field.bast_records (noc_status);

-- Only one *current* BAST per WO at a time. Rejected BASTs stay as
-- history rows; the WO produces a new one on resubmit. We enforce
-- "at most one non-rejected BAST" via a partial unique index.
CREATE UNIQUE INDEX uniq_active_bast_per_wo
    ON field.bast_records (wo_id)
    WHERE noc_status <> 'rejected';

-- =====================================================================
-- 8. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('field', 'wo.read',          'View work orders'),
    ('field', 'wo.create',        'Create work orders from orders/customers'),
    ('field', 'wo.assign',        'Assign / reassign technicians to a WO'),
    ('field', 'wo.update',        'Update WO status, log resolution, submit responses'),
    ('field', 'wo.submit_bast',   'Submit BAST for NOC verification'),
    ('field', 'bast.noc_verify',  'NOC: approve or reject submitted BAST'),
    ('field', 'team.read',        'View field teams'),
    ('field', 'team.manage',      'Create / edit field teams and rosters'),
    ('field', 'checklist.manage', 'Manage WO checklist templates')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: everything
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin' AND p.module = 'field'
ON CONFLICT DO NOTHING;

-- operations_admin: full read + checklist mgmt
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'field'
  AND p.action IN ('wo.read','wo.create','team.read','team.manage','checklist.manage')
ON CONFLICT DO NOTHING;

-- team_leader: assign techs, update status, submit BAST (on behalf)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_leader'
  AND p.module = 'field'
  AND p.action IN ('wo.read','wo.assign','wo.update','wo.submit_bast','team.read')
ON CONFLICT DO NOTHING;

-- technician: read + update (own WO checklist/resolution) + submit BAST
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'field'
  AND p.action IN ('wo.read','wo.update','wo.submit_bast')
ON CONFLICT DO NOTHING;

-- noc / noc_manager: read + final BAST verification
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('noc','noc_manager')
  AND p.module = 'field'
  AND p.action IN ('wo.read','bast.noc_verify','team.read')
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 9. Default checklist template: new_installation × broadband
-- =====================================================================
WITH new_tpl AS (
    INSERT INTO field.wo_checklist_templates (wo_type, product_type, min_photos_required, gps_stamp_on_photos)
    VALUES ('new_installation','broadband', 5, TRUE)
    RETURNING id
)
INSERT INTO field.wo_checklist_template_items (template_id, item_order, item_type, label, required, photo_tag, gps_required, min_accuracy_meters)
SELECT
    (SELECT id FROM new_tpl), item_order, item_type, label, required, photo_tag, gps_required, min_accuracy_meters
FROM (VALUES
    (1, 'photo',        'ONT box — before installation',          TRUE,  'before',        FALSE, NULL),
    (2, 'photo',        'Cable routing on wall',                  TRUE,  'during',        FALSE, NULL),
    (3, 'qr_scan',      'Scan ONT serial number QR',              TRUE,  NULL,            FALSE, NULL),
    (4, 'gps_location', 'Mark installation GPS point',            TRUE,  NULL,            TRUE,  10),
    (5, 'number',       'Signal strength (dBm)',                  TRUE,  NULL,            FALSE, NULL),
    (6, 'photo',        'ONT LED status — after activation',      TRUE,  'after',         FALSE, NULL),
    (7, 'signature',    'Customer signature on completion',       TRUE,  NULL,            FALSE, NULL)
) AS t(item_order, item_type, label, required, photo_tag, gps_required, min_accuracy_meters);

