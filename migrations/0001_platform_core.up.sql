-- 0001 — Platform Core (M1)
--
-- Creates the `identity` schema and seeds the Phase 1 Platform Foundation
-- tables: branches, users, profile extensions, roles/permissions, audit log,
-- platform config. All other contexts will live in their own schemas
-- (admin, crm, billing, network, warehouse, field) added in subsequent
-- migrations.

BEGIN;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE SCHEMA IF NOT EXISTS identity;

-- ----------------------------------------------------------------------
-- Branches — 3-level hierarchy (regional → area → sub_area). Self-referential.
-- ----------------------------------------------------------------------
CREATE TABLE identity.branches (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL,
    code        TEXT NOT NULL UNIQUE,
    level       TEXT NOT NULL CHECK (level IN ('regional','area','sub_area')),
    parent_id   UUID REFERENCES identity.branches(id) ON DELETE RESTRICT,
    geo_polygon JSONB,                 -- GeoJSON polygon for address-to-area resolution
    odp_strategy JSONB,                -- per-branch ODP selection strategy config
    cable_distance JSONB,              -- per-branch cable distance config (max meters, route factor, excess price)
    wo_auto_assign JSONB,              -- per-branch WO auto-assignment SLAs
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Hierarchy invariant enforced by partial CHECK + trigger; here we keep
    -- the basic shape and rely on the application layer for level/parent rules.
    CONSTRAINT branches_regional_no_parent CHECK (
        (level = 'regional' AND parent_id IS NULL) OR
        (level <> 'regional' AND parent_id IS NOT NULL)
    )
);

CREATE INDEX idx_branches_parent ON identity.branches(parent_id);
CREATE INDEX idx_branches_level  ON identity.branches(level);

-- ----------------------------------------------------------------------
-- Users — internal staff. Frontend logs in here.
-- ----------------------------------------------------------------------
CREATE TABLE identity.users (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    employee_id         TEXT UNIQUE,
    full_name           TEXT NOT NULL,
    email               TEXT NOT NULL UNIQUE,
    phone               TEXT,
    password_hash       TEXT NOT NULL,
    reports_to_user_id  UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    branch_id           UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    branch_level        TEXT CHECK (branch_level IS NULL OR branch_level IN ('regional','area','sub_area')),
    active              BOOLEAN NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email      ON identity.users(LOWER(email));
CREATE INDEX idx_users_branch     ON identity.users(branch_id);
CREATE INDEX idx_users_reports_to ON identity.users(reports_to_user_id);

-- ----------------------------------------------------------------------
-- Profile extensions — PK=FK pattern. One row per user only for users in role.
-- ----------------------------------------------------------------------
CREATE TABLE identity.sales_rep_profiles (
    user_id     UUID PRIMARY KEY REFERENCES identity.users(id) ON DELETE CASCADE,
    sales_type  TEXT NOT NULL CHECK (sales_type IN ('broadband','enterprise','both'))
);

CREATE TABLE identity.technician_profiles (
    user_id  UUID PRIMARY KEY REFERENCES identity.users(id) ON DELETE CASCADE,
    grade    TEXT NOT NULL CHECK (grade IN ('senior','junior'))
);

-- ----------------------------------------------------------------------
-- RBAC: roles + permissions + assignment.
-- ----------------------------------------------------------------------
CREATE TABLE identity.roles (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE identity.permissions (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    module      TEXT NOT NULL,
    action      TEXT NOT NULL,
    description TEXT,
    UNIQUE (module, action)
);

CREATE TABLE identity.role_permissions (
    role_id        UUID NOT NULL REFERENCES identity.roles(id) ON DELETE CASCADE,
    permission_id  UUID NOT NULL REFERENCES identity.permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE identity.user_roles (
    user_id       UUID NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    role_id       UUID NOT NULL REFERENCES identity.roles(id) ON DELETE CASCADE,
    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    assigned_by   UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX idx_user_roles_user ON identity.user_roles(user_id);

-- ----------------------------------------------------------------------
-- Audit log — polymorphic by (record_type, record_id). Append-only.
-- ----------------------------------------------------------------------
CREATE TABLE identity.audit_logs (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    timestamp      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    user_id        UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    module         TEXT NOT NULL,
    record_type    TEXT NOT NULL,
    record_id      TEXT NOT NULL,
    field_changed  TEXT,
    before_value   TEXT,
    after_value    TEXT,
    reason         TEXT
);

CREATE INDEX idx_audit_logs_user_time ON identity.audit_logs(user_id, timestamp DESC);
CREATE INDEX idx_audit_logs_record    ON identity.audit_logs(record_type, record_id);

-- ----------------------------------------------------------------------
-- Platform config — global KV.
-- ----------------------------------------------------------------------
CREATE TABLE identity.platform_config (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    config_key  TEXT NOT NULL UNIQUE,
    config_value TEXT NOT NULL,
    updated_by  UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------
-- updated_at trigger — generic touch function.
-- ----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION identity.touch_updated_at() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_branches_touch BEFORE UPDATE ON identity.branches
    FOR EACH ROW EXECUTE FUNCTION identity.touch_updated_at();

CREATE TRIGGER trg_users_touch BEFORE UPDATE ON identity.users
    FOR EACH ROW EXECUTE FUNCTION identity.touch_updated_at();

-- ----------------------------------------------------------------------
-- Seed: canonical Phase 1 roles.
-- Permissions seeded per role as bounded contexts come online.
-- ----------------------------------------------------------------------
INSERT INTO identity.roles (name, description) VALUES
    ('super_admin',        'Full system access'),
    ('operations_admin',   'Branch and user management, platform config'),
    ('product_admin',      'Product catalog, schema builder, WO checklist templates'),
    ('finance_admin',      'Billing schema, commission schema, pricing, tax settings'),
    ('it_admin',           'Integration settings, API keys, notification templates'),
    ('sales_rep',          'Field prospecting, lead and order management, own commission view'),
    ('sales_manager',      'Pipeline oversight, lead takeover, downgrade approval'),
    ('cs_agent',           'Ticket management (deferred to Phase 2)'),
    ('cs_supervisor',      'CS oversight (deferred to Phase 2)'),
    ('noc',                'BAST verification, network monitoring, maintenance WO creation'),
    ('noc_manager',        'NOC + cross-area incident management, War Room (deferred)'),
    ('finance_staff',      'Invoice processing, payment confirmation'),
    ('finance_manager',    'Finance + suspension approval, schema override'),
    ('warehouse_staff',    'Stock dispatch, QR scanning'),
    ('warehouse_manager',  'Warehouse + threshold management, opname'),
    ('team_leader',        'WO assignment for their area, technician pairing'),
    ('technician',         'WO execution, BAST submission (Technical App only)')
ON CONFLICT (name) DO NOTHING;

COMMIT;
