-- M4 CRM Round 1 — Lead → Order → Customer skeleton for broadband.
--
-- We add:
--   crm.products              — minimal package catalog (just enough to pick from)
--   crm.leads                 — prospect captured before they are a customer
--   crm.orders                — created from a converted lead; the install/OTC anchor
--   crm.customers             — created at lead conversion; service+billing anchor
--   crm.order_documents       — document checklist per onboarding-schema-lite
--
-- Deferred to later rounds (do not create as inert stubs here):
--   - schema-driven onboarding (we use a hard-coded broadband checklist for now)
--   - sales rep type enforcement (sales_id is FK to users; type check is later)
--   - invoices/billing (lives in M6)
--   - commission tables (M6)

CREATE SCHEMA IF NOT EXISTS crm;

-- ---------------------------------------------------------------------------
-- 1. crm.products — minimal package catalog
--
-- Schema-driven product catalog (with versioning + draft/publish) is M1
-- platform-foundation territory and was deferred. To unblock M4 we ship
-- a flat table. When the schema-driven catalog lands, this table can be
-- migrated to point at a published schema row, or replaced outright.
-- ---------------------------------------------------------------------------
CREATE TABLE crm.products (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    speed_mbps      INTEGER NOT NULL CHECK (speed_mbps > 0),
    monthly_price   NUMERIC(14,2) NOT NULL CHECK (monthly_price >= 0),
    otc_price       NUMERIC(14,2) NOT NULL CHECK (otc_price >= 0),
    -- temporary_activation_window_hours per PRD plan; how long a NOC-pending
    -- BAST can keep the customer on temporary radius before suspension. Not
    -- yet enforced; surfaced for parity with PRD and consumed later in M2/M6.
    temp_activation_window_hours INTEGER NOT NULL DEFAULT 72,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_crm_products_active ON crm.products (active);

-- ---------------------------------------------------------------------------
-- 2. crm.leads — top of the funnel.
--
-- A lead carries the raw application data and a snapshot of the coverage
-- check result. We persist coverage_snapshot as jsonb (not relational rows)
-- because it is decision-point telemetry: what the coverage engine said
-- *at the moment of capture*. ODPs can later move; the lead's verdict at
-- capture time is what matters for audit and excess-cable consent.
--
-- branch_id is denormalized from coverage_snapshot.best_candidate.branch_id
-- at capture time. It seeds future cross-branch commission detection
-- (infrastructure_branch on installation_node vs sales_branch on sales user).
-- ---------------------------------------------------------------------------
CREATE TABLE crm.leads (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lead_number     TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'new'
        CHECK (status IN ('new','qualified','potential','rejected','converted','lost')),

    -- Identity
    full_name       TEXT NOT NULL,
    phone           TEXT NOT NULL,
    email           TEXT,
    nik             TEXT, -- KTP number (OCR fills later; manual now)

    -- Address
    address         TEXT NOT NULL,
    gps_lat         DOUBLE PRECISION,
    gps_lng         DOUBLE PRECISION,

    -- Coverage decision at capture
    coverage_verdict TEXT
        CHECK (coverage_verdict IS NULL OR coverage_verdict IN ('covered','excess_distance','uncovered')),
    coverage_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    accept_excess_cable BOOLEAN NOT NULL DEFAULT FALSE,
    nearest_node_id  UUID REFERENCES network.nodes(id) ON DELETE SET NULL,
    cable_distance_m NUMERIC(10,2),
    excess_charge    NUMERIC(14,2),

    -- Branch scoping (denormalized from the matched ODP)
    branch_id        UUID REFERENCES identity.branches(id) ON DELETE SET NULL,

    -- Product pick
    product_id       UUID REFERENCES crm.products(id) ON DELETE SET NULL,

    -- Ownership / origination
    sales_id         UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    source           TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual','self_order','sales_app','referral')),
    notes            TEXT,

    -- Conversion linkage (filled on convert; null until then)
    converted_customer_id UUID,
    converted_order_id    UUID,
    converted_at          TIMESTAMPTZ,

    created_by      UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_crm_leads_status      ON crm.leads (status);
CREATE INDEX idx_crm_leads_branch      ON crm.leads (branch_id);
CREATE INDEX idx_crm_leads_sales       ON crm.leads (sales_id);
CREATE INDEX idx_crm_leads_created_at  ON crm.leads (created_at DESC);

-- ---------------------------------------------------------------------------
-- 3. crm.order_documents — onboarding checklist per lead.
--
-- At lead creation we seed a default broadband checklist (KTP, signed_consent
-- when accepting excess cable, GPS_pin, photo_house). The user then attaches
-- documents to satisfy each requirement. Conversion to customer requires
-- every required_doc to be `submitted=true`.
--
-- We store this on the LEAD (not the order) because the gate runs BEFORE the
-- order exists — order is created AS PART OF conversion.
-- ---------------------------------------------------------------------------
CREATE TABLE crm.order_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lead_id         UUID NOT NULL REFERENCES crm.leads(id) ON DELETE CASCADE,
    doc_key         TEXT NOT NULL,
    label           TEXT NOT NULL,
    required        BOOLEAN NOT NULL DEFAULT TRUE,
    submitted       BOOLEAN NOT NULL DEFAULT FALSE,
    file_url        TEXT,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (lead_id, doc_key)
);
CREATE INDEX idx_crm_order_documents_lead ON crm.order_documents (lead_id);

-- ---------------------------------------------------------------------------
-- 4. crm.customers — service + billing anchor.
--
-- Phase 1 only supports customer_type = 'broadband'. Other types remain
-- inert/stubbed per the plan. installation_node_id is set at conversion
-- so commission's infrastructure_branch split can be computed later.
-- ---------------------------------------------------------------------------
CREATE TABLE crm.customers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_number TEXT NOT NULL UNIQUE,
    customer_type   TEXT NOT NULL DEFAULT 'broadband'
        CHECK (customer_type IN ('broadband','business','enterprise','corporate')),
    full_name       TEXT NOT NULL,
    phone           TEXT NOT NULL,
    email           TEXT,
    nik             TEXT,
    address         TEXT NOT NULL,
    gps_lat         DOUBLE PRECISION,
    gps_lng         DOUBLE PRECISION,
    branch_id       UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    installation_node_id UUID REFERENCES network.nodes(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'pending_install'
        CHECK (status IN ('pending_install','active','suspended','terminated')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_crm_customers_status ON crm.customers (status);
CREATE INDEX idx_crm_customers_branch ON crm.customers (branch_id);

-- ---------------------------------------------------------------------------
-- 5. crm.orders — install order (and OTC anchor) per converted lead.
--
-- Phase 1 scope: one order per conversion. The order is what M5 (Field)
-- and M6 (Billing) attach to: the WO will reference order_id, and OTC
-- invoices will reference order_id.
-- ---------------------------------------------------------------------------
CREATE TABLE crm.orders (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_number    TEXT NOT NULL UNIQUE,
    lead_id         UUID REFERENCES crm.leads(id) ON DELETE SET NULL,
    customer_id     UUID NOT NULL REFERENCES crm.customers(id) ON DELETE RESTRICT,
    product_id      UUID REFERENCES crm.products(id) ON DELETE SET NULL,

    -- Snapshot of price at order time. Even if the product price later
    -- changes, the order is the contract for what was sold.
    monthly_price   NUMERIC(14,2) NOT NULL,
    otc_price       NUMERIC(14,2) NOT NULL,
    excess_charge   NUMERIC(14,2) NOT NULL DEFAULT 0,

    accept_excess_cable BOOLEAN NOT NULL DEFAULT FALSE,
    nearest_node_id     UUID REFERENCES network.nodes(id) ON DELETE SET NULL,
    branch_id           UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    sales_id            UUID REFERENCES identity.users(id) ON DELETE SET NULL,

    status          TEXT NOT NULL DEFAULT 'created'
        CHECK (status IN ('created','wo_assigned','installed','active','cancelled')),
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_crm_orders_customer ON crm.orders (customer_id);
CREATE INDEX idx_crm_orders_status   ON crm.orders (status);

-- Now wire the deferred FKs we couldn't set at lead-create time.
ALTER TABLE crm.leads
  ADD CONSTRAINT leads_converted_customer_fk FOREIGN KEY (converted_customer_id)
      REFERENCES crm.customers(id) ON DELETE SET NULL,
  ADD CONSTRAINT leads_converted_order_fk    FOREIGN KEY (converted_order_id)
      REFERENCES crm.orders(id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------------
-- 6. Permission seeds
--
-- Keys are constructed as <module>.<action> by the permission resolver
-- (see PermissionsForUser). For CRM: module='crm', action='lead.read', etc.
-- ---------------------------------------------------------------------------
INSERT INTO identity.permissions (module, action, description) VALUES
    ('crm', 'lead.read',      'View leads and their documents/coverage snapshot'),
    ('crm', 'lead.manage',    'Create/update leads, attach docs, mark accept-excess'),
    ('crm', 'lead.convert',   'Convert qualified lead to customer + order'),
    ('crm', 'product.read',   'List products in catalog'),
    ('crm', 'product.manage', 'Manage product catalog'),
    ('crm', 'customer.read',  'View customers'),
    ('crm', 'order.read',     'View orders')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: everything
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin' AND p.module = 'crm'
ON CONFLICT DO NOTHING;

-- operations_admin: read-only across the CRM surface
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'crm'
  AND p.action IN ('lead.read','product.read','customer.read','order.read')
ON CONFLICT DO NOTHING;

-- sales_manager: full lead lifecycle + read
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'crm'
  AND p.action IN ('lead.read','lead.manage','lead.convert','product.read','customer.read','order.read')
ON CONFLICT DO NOTHING;

-- sales_rep: own/assigned leads, read products and (their) customers/orders
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'crm'
  AND p.action IN ('lead.read','lead.manage','product.read','customer.read','order.read')
ON CONFLICT DO NOTHING;

-- product_admin: catalog management
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'product_admin'
  AND p.module = 'crm'
  AND p.action IN ('product.read','product.manage')
ON CONFLICT DO NOTHING;

-- Seed a couple of broadband products so M4 round 1 has something to pick.
INSERT INTO crm.products (code, name, speed_mbps, monthly_price, otc_price) VALUES
    ('BB-10',  '10 Mbps Home',  10,  150000, 250000),
    ('BB-30',  '30 Mbps Home',  30,  250000, 250000),
    ('BB-50',  '50 Mbps Home',  50,  350000, 250000),
    ('BB-100', '100 Mbps Home', 100, 500000, 350000)
ON CONFLICT (code) DO NOTHING;
