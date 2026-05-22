-- 0004 — Network & Orchestration (M2 foundation).
--
-- Creates the `network` schema and the physical topology registry per PRD §5:
--
--   POP → ODC* → ODP → ONT  (fiber path)
--   POP → MikroTik → Switch (routing path)
--
-- Every node carries branch_id (any branch level), an optional asset_id
-- (warehouse record — added later as warehouse module lands), and an
-- upstream_port_id pointing to the specific port on its parent the node
-- connects through. That last FK is circular with `ports.node_id`, so we
-- add it via ALTER TABLE after both tables exist.
--
-- This migration also stubs out the support tables that will be wired by
-- future code: radius_accounts (1:1 with customer), vlan_pools, ip_pools,
-- ip_assignments. They sit empty until the ION Radius integration is built.

BEGIN;

CREATE SCHEMA IF NOT EXISTS network;

-- ----------------------------------------------------------------------
-- Node type catalog — configurable, no code change needed to add a new type.
-- ----------------------------------------------------------------------
CREATE TABLE network.node_types (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type_key      TEXT NOT NULL UNIQUE,
    label         TEXT NOT NULL,
    description   TEXT,
    icon_online   TEXT,
    icon_offline  TEXT,
    icon_trouble  TEXT,
    sort_order    INT NOT NULL DEFAULT 0,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------
-- Nodes — hierarchical tree via parent_id.
-- ----------------------------------------------------------------------
CREATE TABLE network.nodes (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    node_type_id      UUID NOT NULL REFERENCES network.node_types(id) ON DELETE RESTRICT,
    name              TEXT NOT NULL,
    code              TEXT NOT NULL UNIQUE,
    parent_id         UUID REFERENCES network.nodes(id) ON DELETE RESTRICT,
    upstream_port_id  UUID,  -- FK added at bottom (circular with ports.node_id)
    branch_id         UUID REFERENCES identity.branches(id) ON DELETE RESTRICT,
    asset_id          UUID,  -- FK added when warehouse module lands
    address           TEXT,
    gps_lat           DOUBLE PRECISION,
    gps_lng           DOUBLE PRECISION,
    coverage_radius_m INT,
    total_ports       INT,
    status            TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','degraded','down','maintenance','full','decommissioned')),
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
    active            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_network_nodes_parent      ON network.nodes(parent_id);
CREATE INDEX idx_network_nodes_branch      ON network.nodes(branch_id);
CREATE INDEX idx_network_nodes_type        ON network.nodes(node_type_id);
CREATE INDEX idx_network_nodes_gps         ON network.nodes(gps_lat, gps_lng)
    WHERE gps_lat IS NOT NULL AND gps_lng IS NOT NULL;

-- ----------------------------------------------------------------------
-- Ports — unified across all node types (PRD decision).
-- ----------------------------------------------------------------------
CREATE TABLE network.ports (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    node_id             UUID NOT NULL REFERENCES network.nodes(id) ON DELETE CASCADE,
    port_number         INT NOT NULL,
    port_role           TEXT NOT NULL
        CHECK (port_role IN ('pon_downlink','distribution_input','distribution_output','customer_drop','uplink','generic')),
    max_capacity        INT NOT NULL DEFAULT 1,
    active_connections  INT NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'available'
        CHECK (status IN ('available','reserved','active','faulty')),
    customer_id         UUID,  -- FK to crm.customers added when CRM module lands
    reserved_for        UUID,  -- pending customer reference
    reserved_until      TIMESTAMPTZ,
    activated_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (node_id, port_number)
);

CREATE INDEX idx_network_ports_node      ON network.ports(node_id);
CREATE INDEX idx_network_ports_status    ON network.ports(status);
CREATE INDEX idx_network_ports_customer  ON network.ports(customer_id)
    WHERE customer_id IS NOT NULL;

-- ----------------------------------------------------------------------
-- Close the circular FK: nodes.upstream_port_id → ports.id
-- ----------------------------------------------------------------------
ALTER TABLE network.nodes
    ADD CONSTRAINT fk_nodes_upstream_port
    FOREIGN KEY (upstream_port_id) REFERENCES network.ports(id) ON DELETE SET NULL;

-- ----------------------------------------------------------------------
-- RADIUS accounts — 1:1 with a customer. Filled by the ION Radius adapter.
-- ----------------------------------------------------------------------
CREATE TABLE network.radius_accounts (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id          UUID NOT NULL UNIQUE,  -- FK to crm.customers later
    username             TEXT NOT NULL UNIQUE,
    password_encrypted   TEXT NOT NULL,
    vlan_id              INT,
    bandwidth_profile_id TEXT,
    ip_address           INET,
    status               TEXT NOT NULL DEFAULT 'temporary'
        CHECK (status IN ('temporary','permanent_active','suspended','deactivated')),
    temp_activated_at    TIMESTAMPTZ,
    perm_activated_at    TIMESTAMPTZ,
    suspended_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_radius_accounts_status ON network.radius_accounts(status);

-- ----------------------------------------------------------------------
-- VLAN pools per area (enterprise allocation).
-- ----------------------------------------------------------------------
CREATE TABLE network.vlan_pools (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    branch_id   UUID NOT NULL REFERENCES identity.branches(id) ON DELETE RESTRICT,
    vlan_start  INT NOT NULL CHECK (vlan_start BETWEEN 1 AND 4094),
    vlan_end    INT NOT NULL CHECK (vlan_end   BETWEEN 1 AND 4094),
    notes       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (vlan_start <= vlan_end)
);

-- ----------------------------------------------------------------------
-- IP pools per area, separated by customer_type + pool_type.
-- ----------------------------------------------------------------------
CREATE TABLE network.ip_pools (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    branch_id       UUID NOT NULL REFERENCES identity.branches(id) ON DELETE RESTRICT,
    customer_type   TEXT NOT NULL CHECK (customer_type IN ('broadband','business','enterprise','corporate')),
    pool_type       TEXT NOT NULL CHECK (pool_type IN ('dynamic','static')),
    ip_range_start  INET NOT NULL,
    ip_range_end    INET NOT NULL,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------
-- IP assignments (live allocation tracking).
-- ----------------------------------------------------------------------
CREATE TABLE network.ip_assignments (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id  UUID NOT NULL,  -- FK to crm.customers later
    pool_id      UUID NOT NULL REFERENCES network.ip_pools(id) ON DELETE RESTRICT,
    ip_address   INET NOT NULL,
    assigned_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at  TIMESTAMPTZ
);

CREATE INDEX idx_ip_assignments_customer ON network.ip_assignments(customer_id);
CREATE UNIQUE INDEX idx_ip_assignments_active_ip
    ON network.ip_assignments(ip_address) WHERE released_at IS NULL;

-- ----------------------------------------------------------------------
-- updated_at trigger
-- ----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION network.touch_updated_at() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_network_nodes_touch
    BEFORE UPDATE ON network.nodes
    FOR EACH ROW EXECUTE FUNCTION network.touch_updated_at();

CREATE TRIGGER trg_radius_accounts_touch
    BEFORE UPDATE ON network.radius_accounts
    FOR EACH ROW EXECUTE FUNCTION network.touch_updated_at();

-- ----------------------------------------------------------------------
-- Seed node types per PRD §5.2 (admin can add more without code changes).
-- icon_* values are slot names — the FE swaps them for actual icon graphics.
-- ----------------------------------------------------------------------
INSERT INTO network.node_types (type_key, label, description, icon_online, icon_offline, icon_trouble, sort_order) VALUES
    ('internet_source', 'Internet Source', 'Upstream transit / peering / IX gateway',     'cloud-online',   'cloud-offline',   'cloud-warning',   10),
    ('pop',             'POP',              'Point of Presence — physical site container', 'building-online','building-offline','building-warning',20),
    ('olt',             'OLT',              'Optical Line Terminal',                        'server-online',  'server-offline',  'server-warning',  30),
    ('odc',             'ODC',              'Optical Distribution Cabinet',                 'cabinet-online', 'cabinet-offline', 'cabinet-warning', 40),
    ('splitter',        'Splitter',         'Passive optical splitter',                     'splitter-online','splitter-offline','splitter-warning',50),
    ('odp',             'ODP',              'Optical Distribution Point (customer drop)',   'box-online',     'box-offline',     'box-warning',     60),
    ('ont',             'ONT',              'Customer Optical Network Terminal',            'modem-online',   'modem-offline',  'modem-warning',   70),
    ('mikrotik',        'MikroTik',         'MikroTik router',                              'router-online',  'router-offline',  'router-warning',  80),
    ('switch',          'Switch',           'Network switch',                               'switch-online',  'switch-offline',  'switch-warning',  90),
    ('router',          'Router',           'Generic router',                               'router-online',  'router-offline',  'router-warning',  100),
    ('other',           'Other',            'Catch-all for uncategorized network devices',  'device-online',  'device-offline',  'device-warning',  999)
ON CONFLICT (type_key) DO NOTHING;

COMMIT;
