-- 0038 — Gap-closure batch.
--
-- Closes the highest-severity gaps from docs/gap-analysis.md that need
-- DB-shape work. All additive — no destructive changes.
--
-- New columns:
--   field.work_orders.journey_started_at    (Tech app "Start Journey")
--   field.work_orders.arrived_at            (Tech app "Arrived")
--   field.tickets.last_message_at           (CS inbox sort key)
--
-- New tables:
--   field.ticket_messages                   (timeline of agent ↔ customer replies)
--   enterprise.project_plan_revisions       (version history for the project plan)
--
-- Seed inserts:
--   enterprise.enterprise_services         (CCTV, Data Center, Managed Wi-Fi, etc.)

BEGIN;

-- =====================================================================
-- 1. Tech-app journey timestamps + arrived state
-- =====================================================================
ALTER TABLE field.work_orders
    ADD COLUMN IF NOT EXISTS journey_started_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS arrived_at          TIMESTAMPTZ;

-- =====================================================================
-- 2. CS ticket messages — the conversation timeline
-- =====================================================================
CREATE TABLE IF NOT EXISTS field.ticket_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id       UUID NOT NULL REFERENCES field.tickets(id) ON DELETE CASCADE,
    author_kind     TEXT NOT NULL CHECK (author_kind IN ('agent','customer','system')),
    author_user_id  UUID,                          -- identity.users when agent
    author_customer_id UUID,                       -- crm.customers when customer
    body            TEXT NOT NULL,
    is_internal_note BOOLEAN NOT NULL DEFAULT FALSE,
    attachments_url TEXT[],
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ticket_messages_ticket
    ON field.ticket_messages (ticket_id, created_at);

-- Convenience column on tickets for inbox sorting.
ALTER TABLE field.tickets
    ADD COLUMN IF NOT EXISTS last_message_at TIMESTAMPTZ;

-- =====================================================================
-- 3. Enterprise project plan revisions (version history)
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.project_plan_revisions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL,                 -- enterprise.projects (soft FK)
    revision_no     INT NOT NULL,
    snapshot_json   JSONB NOT NULL,                -- full milestone+s-curve plan at time of revision
    reason          TEXT,
    revised_by      UUID,                          -- identity.users
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, revision_no)
);
CREATE INDEX IF NOT EXISTS idx_project_plan_revs_project
    ON enterprise.project_plan_revisions (project_id, revision_no DESC);

-- =====================================================================
-- 4. Extended enterprise service catalog (PRD §4.1 line 5121).
--
-- Note: enterprise.enterprise_services already exists but tracks
-- activated services per project_site. The catalog is its own table.
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.service_catalog (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    category        TEXT NOT NULL CHECK (category IN (
        'connectivity','security','entertainment','data_center',
        'managed_services','infrastructure'
    )),
    description     TEXT,
    delivery_type   TEXT NOT NULL CHECK (delivery_type IN (
        'ion_direct','vendor_supplied','hybrid'
    )),
    unit            TEXT NOT NULL CHECK (unit IN (
        'monthly','one_time','per_unit','per_m2','per_rack'
    )),
    base_price      NUMERIC(14,2) NOT NULL DEFAULT 0,
    pricing_type    TEXT NOT NULL DEFAULT 'fixed' CHECK (pricing_type IN (
        'fixed','negotiated','vendor_quoted'
    )),
    sla_template_id UUID,
    requires_wo     BOOLEAN NOT NULL DEFAULT FALSE,
    wo_type         TEXT,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_service_catalog_active
    ON enterprise.service_catalog (active) WHERE active;
CREATE INDEX IF NOT EXISTS idx_service_catalog_category
    ON enterprise.service_catalog (category);

INSERT INTO enterprise.service_catalog
    (code, name, category, description, delivery_type, unit, base_price, pricing_type, requires_wo, wo_type, active)
VALUES
    ('SVC_DED_INTERNET',  'Dedicated Internet (1G symmetric)', 'connectivity', 'Layer-3 dedicated link, BGP-capable. SLA 99.9%.', 'ion_direct',   'monthly', 25000000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_METRO_E',       'Metro Ethernet (point-to-point)',    'connectivity', 'Layer-2 metro between two sites within ION footprint.', 'ion_direct', 'monthly', 18000000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_MPLS',          'MPLS VPN (multi-site)',              'connectivity', 'Layer-3 MPLS VPN; min 3 sites.', 'ion_direct', 'monthly', 12000000, 'negotiated', TRUE, 'installation', TRUE),
    ('SVC_CCTV_KIT',      'CCTV — 4-camera kit + NVR',          'security',     '4 IP cameras, NVR with 30-day storage. Cabling included.', 'vendor_supplied', 'one_time', 8500000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_CCTV_MGMT',     'CCTV — managed monitoring',          'security',     '24/7 monitoring + alert escalation.', 'ion_direct', 'monthly', 1500000, 'fixed', FALSE, NULL, TRUE),
    ('SVC_IPTV',          'IPTV — Enterprise package',          'entertainment', '120+ channels + lobby signage feed.', 'ion_direct', 'monthly', 750000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_DC_COLO',       'Data Center co-location (1/2 rack)', 'data_center',  'Half-rack, 5kW power, 100Mbps uplink.', 'hybrid', 'monthly', 9000000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_DC_RACK',       'Data Center full rack',              'data_center',  'Full 42U rack, 10kW power, 1Gbps uplink, BGP-ready.', 'hybrid', 'monthly', 17500000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_CLOUD_IXP',     'IXP cloud connect',                  'data_center',  'Direct cloud peering via OpenIXP / IIX.', 'ion_direct', 'monthly', 4500000, 'fixed', FALSE, NULL, TRUE),
    ('SVC_MWIFI',         'Managed Wi-Fi (per AP)',             'managed_services', 'Enterprise AP + controller + monitoring.', 'ion_direct', 'per_unit', 350000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_MROUTER',       'Managed router / switch',            'managed_services', 'Customer CPE with config + monitoring.', 'ion_direct', 'monthly', 500000, 'fixed', FALSE, NULL, TRUE),
    ('SVC_CABLING',       'Structured cabling (per meter)',     'infrastructure', 'Cat6A + termination + labeling.', 'hybrid', 'per_unit', 35000, 'fixed', TRUE, 'installation', TRUE),
    ('SVC_FIBER_BB',      'Fiber backbone (point-to-point)',    'infrastructure', 'Dark or lit fiber between two sites.', 'ion_direct', 'monthly', 6500000, 'negotiated', TRUE, 'installation', TRUE)
ON CONFLICT (code) DO NOTHING;

-- =====================================================================
-- 5. Permissions for the new web pages
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('field.ticket', 'reply',  'Reply on a CS ticket'),
    ('crm.commission', 'read.own', 'Read own commission records (sales rep self-view)')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('super_admin','operations_admin','noc')
  AND p.module || '.' || p.action IN ('field.ticket.reply')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('sales_rep','sales_manager','super_admin','operations_admin')
  AND p.module || '.' || p.action IN ('crm.commission.read.own')
ON CONFLICT DO NOTHING;

COMMIT;
