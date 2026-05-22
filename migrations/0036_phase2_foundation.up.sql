-- 0036 — Phase 2 foundation.
--
-- Lays the schema for the post-activation product surface plus the
-- CS-ticket and maintenance plumbing the mobile apps need. Business
-- logic for each capability lands service-side; this migration is the
-- empty container.
--
-- New tables (all FK to existing crm/field/identity entities):
--   1. crm.product_addons               — catalog of buyable add-ons
--   2. crm.customer_addons              — customer-side purchases
--   3. crm.plan_change_requests         — upgrade/downgrade workflow
--   4. crm.customer_relocations         — address change workflow
--   5. field.tickets                    — CS module skeleton
--   6. field.maintenance_events         — scheduled / preventive WO trigger
--   7. field.maintenance_event_nodes    — which nodes a maintenance event touches
--
-- Schema-only changes:
--   - field.wo_checklist_template_items.item_type CHECK adds 'optical_power'
--   - field.work_orders.ticket_id (soft FK to field.tickets)
--   - field.work_orders.maintenance_event_id (soft FK)
--
-- Idempotency: all CREATE TABLE use IF NOT EXISTS; the CHECK rewrite
-- uses a guarded DO block so the down migration can restore.

BEGIN;

-- =====================================================================
-- 1. crm.product_addons — catalog
-- =====================================================================
CREATE TABLE IF NOT EXISTS crm.product_addons (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    addon_type      TEXT NOT NULL CHECK (addon_type IN (
        'speed_boost','iptv','cctv','static_ip','wifi_extender','other'
    )),
    -- Price model is simple in MVP. Recurring = monthly; one_time = setup fee.
    one_time_fee    NUMERIC(14,2) NOT NULL DEFAULT 0,
    monthly_fee     NUMERIC(14,2) NOT NULL DEFAULT 0,
    -- For speed_boost: the new bandwidth profile to apply (resolves via
    -- network.bandwidth_profiles). For others: null.
    bandwidth_profile_id  UUID,
    -- Whether buying this add-on requires a tech to physically come on-site
    -- (CCTV/wifi_extender) versus a config-only flip (speed_boost).
    requires_install      BOOLEAN NOT NULL DEFAULT FALSE,
    active                BOOLEAN NOT NULL DEFAULT TRUE,
    description           TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_product_addons_active ON crm.product_addons (active) WHERE active;
CREATE INDEX IF NOT EXISTS idx_product_addons_type ON crm.product_addons (addon_type);

-- =====================================================================
-- 2. crm.customer_addons — customer's active + historical purchases
-- =====================================================================
CREATE TABLE IF NOT EXISTS crm.customer_addons (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL,                    -- crm.customers (soft FK across svc boundary)
    addon_id        UUID NOT NULL REFERENCES crm.product_addons(id) ON DELETE RESTRICT,
    sales_rep_id    UUID,                             -- identity.users (who sold it)
    status          TEXT NOT NULL DEFAULT 'pending_install' CHECK (status IN (
        'pending_install','active','suspended','cancelled'
    )),
    quantity        INT NOT NULL DEFAULT 1 CHECK (quantity > 0),
    one_time_fee    NUMERIC(14,2) NOT NULL DEFAULT 0,
    monthly_fee     NUMERIC(14,2) NOT NULL DEFAULT 0,
    -- Optional WO created when requires_install=true.
    install_wo_id   UUID,
    notes           TEXT,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    activated_at    TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_customer_addons_customer ON crm.customer_addons (customer_id);
CREATE INDEX IF NOT EXISTS idx_customer_addons_status ON crm.customer_addons (status);
CREATE INDEX IF NOT EXISTS idx_customer_addons_sales ON crm.customer_addons (sales_rep_id);

-- =====================================================================
-- 3. crm.plan_change_requests — upgrade / downgrade workflow
-- =====================================================================
CREATE TABLE IF NOT EXISTS crm.plan_change_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL,
    -- The old and new products are crm.products; we record both at the
    -- moment of request so historical reporting is unaffected by future
    -- catalog changes.
    from_product_id UUID NOT NULL,
    to_product_id   UUID NOT NULL,
    change_kind     TEXT NOT NULL CHECK (change_kind IN ('upgrade','downgrade')),
    reason          TEXT,
    sales_rep_id    UUID,                             -- identity.users
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending','approved','rejected','applied','cancelled'
    )),
    -- Effective date — when the new product / bandwidth profile takes effect.
    -- Approval can set this; default is "next billing cycle".
    effective_at    TIMESTAMPTZ,
    applied_at      TIMESTAMPTZ,
    decided_by      UUID,                             -- identity.users (manager)
    decision_note   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_plan_changes_customer ON crm.plan_change_requests (customer_id);
CREATE INDEX IF NOT EXISTS idx_plan_changes_status ON crm.plan_change_requests (status);

-- =====================================================================
-- 4. crm.customer_relocations — address change workflow
-- =====================================================================
CREATE TABLE IF NOT EXISTS crm.customer_relocations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id         UUID NOT NULL,
    from_address        TEXT NOT NULL,
    to_address          TEXT NOT NULL,
    to_gps_lat          DOUBLE PRECISION,
    to_gps_lng          DOUBLE PRECISION,
    sales_rep_id        UUID,                         -- identity.users
    status              TEXT NOT NULL DEFAULT 'pending_survey' CHECK (status IN (
        'pending_survey','survey_failed','approved','rejected',
        'install_wo_open','completed','cancelled'
    )),
    -- Workflow:
    --   pending_survey → coverage check at new address (NOC / Tech)
    --     → survey_failed (no coverage) OR approved
    --   approved → new install_wo (field.work_orders) opens
    --   completed when the install WO closes
    install_wo_id       UUID,                         -- field.work_orders
    survey_note         TEXT,
    decided_by          UUID,
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    surveyed_at         TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_relocations_customer ON crm.customer_relocations (customer_id);
CREATE INDEX IF NOT EXISTS idx_relocations_status ON crm.customer_relocations (status);

-- =====================================================================
-- 5. field.tickets — CS module skeleton
-- =====================================================================
CREATE TABLE IF NOT EXISTS field.tickets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_number   TEXT NOT NULL UNIQUE,
    customer_id     UUID NOT NULL,                    -- crm.customers (soft FK)
    category        TEXT NOT NULL CHECK (category IN (
        'no_internet','slow_speed','frequent_drops','equipment_damage',
        'billing_dispute','other'
    )),
    priority        TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN (
        'high','medium','low'
    )),
    status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN (
        'open','in_progress','pending_customer','resolved','closed'
    )),
    summary         TEXT NOT NULL,
    description     TEXT,
    opened_by       UUID,                             -- identity.users (CS agent)
    assigned_to     UUID,                             -- identity.users (CS / technician)
    -- Link to a WO if a field visit is needed. Most tickets resolve
    -- remotely; only some spawn a maintenance WO.
    wo_id           UUID,                             -- field.work_orders (soft FK)
    -- SLA targets driven by category × priority. We snapshot the target
    -- at ticket open time so SLA reporting doesn't shift if policy changes.
    sla_response_due TIMESTAMPTZ,
    sla_resolve_due  TIMESTAMPTZ,
    resolved_at      TIMESTAMPTZ,
    closed_at        TIMESTAMPTZ,
    csat_score       INT CHECK (csat_score BETWEEN 1 AND 5),
    csat_comment     TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tickets_customer ON field.tickets (customer_id);
CREATE INDEX IF NOT EXISTS idx_tickets_status ON field.tickets (status) WHERE status NOT IN ('resolved','closed');
CREATE INDEX IF NOT EXISTS idx_tickets_assigned ON field.tickets (assigned_to) WHERE assigned_to IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tickets_wo ON field.tickets (wo_id) WHERE wo_id IS NOT NULL;

-- =====================================================================
-- 6. field.maintenance_events — scheduled / preventive WO trigger
-- =====================================================================
CREATE TABLE IF NOT EXISTS field.maintenance_events (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_code          TEXT NOT NULL UNIQUE,
    title               TEXT NOT NULL,
    description         TEXT,
    -- 'preventive' = scheduled fiber clean; 'corrective' = NOC-triggered
    -- after attenuation drift detected; 'planned_outage' = announced.
    event_kind          TEXT NOT NULL CHECK (event_kind IN (
        'preventive','corrective','planned_outage'
    )),
    scheduled_start     TIMESTAMPTZ NOT NULL,
    scheduled_end       TIMESTAMPTZ,
    status              TEXT NOT NULL DEFAULT 'planned' CHECK (status IN (
        'planned','dispatched','in_progress','completed','cancelled'
    )),
    -- Maintenance is always branch-scoped (regional / area / sub_area).
    branch_id           UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    assigned_team_id    UUID REFERENCES field.teams(id) ON DELETE SET NULL,
    -- When the event spawns one or more WOs, we capture them via the
    -- field.work_orders.maintenance_event_id FK below (1-to-many).
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_maint_events_branch ON field.maintenance_events (branch_id);
CREATE INDEX IF NOT EXISTS idx_maint_events_status ON field.maintenance_events (status);
CREATE INDEX IF NOT EXISTS idx_maint_events_scheduled ON field.maintenance_events (scheduled_start);

-- =====================================================================
-- 7. field.maintenance_event_nodes — which network nodes are touched
-- (Phase 1 had the columns inert; we surface the relation here so the
--  mobile app can show "this maintenance covers ODP X, Y, Z").
-- =====================================================================
CREATE TABLE IF NOT EXISTS field.maintenance_event_nodes (
    event_id        UUID NOT NULL REFERENCES field.maintenance_events(id) ON DELETE CASCADE,
    node_id         UUID NOT NULL,                    -- network.network_nodes (soft FK)
    note            TEXT,
    PRIMARY KEY (event_id, node_id)
);

-- =====================================================================
-- 8. Link WO → ticket / maintenance_event (soft FK columns)
-- =====================================================================
ALTER TABLE field.work_orders
    ADD COLUMN IF NOT EXISTS ticket_id            UUID,
    ADD COLUMN IF NOT EXISTS maintenance_event_id UUID;

CREATE INDEX IF NOT EXISTS idx_work_orders_ticket
    ON field.work_orders (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_work_orders_maint
    ON field.work_orders (maintenance_event_id) WHERE maintenance_event_id IS NOT NULL;

-- =====================================================================
-- 9. Checklist `optical_power` item_type — for fiber attenuation reading.
--
-- We can't just ALTER CHECK in PostgreSQL — drop + add. Keep the SET
-- ordering the same as the original to minimise diff noise.
-- =====================================================================
ALTER TABLE field.wo_checklist_template_items
    DROP CONSTRAINT IF EXISTS wo_checklist_template_items_item_type_check;
ALTER TABLE field.wo_checklist_template_items
    ADD CONSTRAINT wo_checklist_template_items_item_type_check
    CHECK (item_type IN (
        'photo','text','number','checkbox','qr_scan','signature','gps_location','optical_power'
    ));

-- =====================================================================
-- 10. Permissions — new keys that mobile + web gate on. Seed into
-- identity.permissions (module/action shape, not single-key).
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('crm.addon',         'read',       'Read product add-ons catalog'),
    ('crm.addon',         'manage',     'Manage add-on catalog (admin)'),
    ('crm.addon',         'sell',       'Sell add-on to existing customer'),
    ('crm.plan_change',   'create',     'Submit plan change request'),
    ('crm.plan_change',   'decide',     'Approve or reject plan change'),
    ('crm.relocation',    'create',     'Submit customer relocation request'),
    ('crm.relocation',    'decide',     'Approve or reject relocation'),
    ('field.ticket',      'read',       'Read CS tickets'),
    ('field.ticket',      'create',     'Open a CS ticket'),
    ('field.ticket',      'assign',     'Assign a CS ticket'),
    ('field.ticket',      'resolve',    'Resolve / close a CS ticket'),
    ('field.maintenance', 'read',       'Read maintenance events'),
    ('field.maintenance', 'create',     'Create maintenance event'),
    ('field.maintenance', 'dispatch',   'Dispatch a maintenance event to a team')
ON CONFLICT (module, action) DO NOTHING;

-- Grant the new keys to super_admin via role_permissions. (Other roles
-- are wired through the admin UI by an operator.)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module || '.' || p.action IN (
    'crm.addon.read','crm.addon.manage','crm.addon.sell',
    'crm.plan_change.create','crm.plan_change.decide',
    'crm.relocation.create','crm.relocation.decide',
    'field.ticket.read','field.ticket.create',
    'field.ticket.assign','field.ticket.resolve',
    'field.maintenance.read','field.maintenance.create','field.maintenance.dispatch'
)
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 11. Seed a couple of starter add-ons so the catalog isn't empty on
-- first boot. Operators can manage the rest via the admin UI.
-- =====================================================================
INSERT INTO crm.product_addons (code, name, addon_type, one_time_fee, monthly_fee, requires_install, description)
VALUES
    ('SPEED_BOOST_2X',  'Speed Boost (2× existing)', 'speed_boost', 0,       50000,  FALSE,
     'Doubles the customer''s downstream speed. Applied as a bandwidth-profile flip; no on-site visit.'),
    ('IPTV_BASIC',      'IPTV — Basic package',     'iptv',        150000,  100000, TRUE,
     '50+ channels. Requires an STB on-site install.'),
    ('CCTV_4CAM',       'CCTV bundle — 4 cameras',  'cctv',        2500000, 75000,  TRUE,
     '4-cam outdoor kit + NVR. Includes mounting + cable run.'),
    ('STATIC_IP',       'Static public IPv4',       'static_ip',   100000,  35000,  FALSE,
     'Single static IPv4 routed to the customer''s WAN. Config-only.'),
    ('WIFI_EXTENDER',   'Mesh WiFi extender',       'wifi_extender', 350000, 0,     TRUE,
     'Adds a single mesh node. Tech runs a quick coverage check.')
ON CONFLICT (code) DO NOTHING;

COMMIT;
