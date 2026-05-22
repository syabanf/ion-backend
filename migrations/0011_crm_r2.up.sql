-- M4 Round 2 — Schema-driven onboarding + sales-type enforcement.
--
-- Round-2 scope:
--   * Onboarding schemas: replace the hardcoded `DefaultBroadbandDocs`
--     with versioned, per-(customer_type × product_type) schemas stored
--     as jsonb. M1 PRD calls for 5 schema types with draft/publish; we
--     ship just the onboarding schema here and only the published
--     state (draft/publish workflow can be added later — published
--     content lives in `content` jsonb).
--
--   * Sales-type enforcement is service-level only (no schema change);
--     the migration just adds an audit-friendly column on leads so we
--     can see which sales user's type was used at lead creation.
--
-- Deferred to round-3 (not created here):
--   * Self-order portal (separate auth surface)
--   * KTP OCR Mode A/B (needs OCR vendor + image storage)
--   * Other schema types (billing, service, commission, suspension)
--     — those live in their respective contexts (M6).

-- =====================================================================
-- 1. Onboarding schemas
--
-- One published schema per (customer_type, product_type). The
-- `content` jsonb holds the list of required documents:
--
--   {
--     "version": 1,
--     "documents": [
--       { "key": "ktp_id",  "label": "KTP / National ID", "required": true,
--         "show_when_accept_excess": null },
--       { "key": "excess_cable_consent",
--         "label": "Signed excess-cable consent", "required": true,
--         "show_when_accept_excess": true }
--     ]
--   }
--
-- The `show_when_accept_excess` field lets a doc slot be conditional
-- on the lead's accept_excess_cable flag. NULL = always shown.
-- =====================================================================
CREATE TABLE crm.onboarding_schemas (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_type   TEXT NOT NULL CHECK (customer_type IN ('broadband','business','enterprise','corporate')),
    product_type    TEXT NOT NULL DEFAULT 'standard',
    version         INT  NOT NULL DEFAULT 1,
    content         JSONB NOT NULL,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    notes           TEXT,
    created_by      UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (customer_type, product_type, version)
);
CREATE INDEX idx_onboarding_active
    ON crm.onboarding_schemas (customer_type, product_type)
    WHERE active;

-- Seed the broadband × standard schema so the convert flow stops using
-- the hardcoded default. Mirror DefaultBroadbandDocs from r1.
INSERT INTO crm.onboarding_schemas (customer_type, product_type, version, content, active, notes)
VALUES (
    'broadband', 'standard', 1,
    '{
      "version": 1,
      "documents": [
        { "key": "ktp_id",               "label": "KTP / National ID",        "required": true,  "show_when_accept_excess": null },
        { "key": "address_proof",        "label": "Address proof",            "required": true,  "show_when_accept_excess": null },
        { "key": "house_photo",          "label": "House photo",              "required": false, "show_when_accept_excess": null },
        { "key": "gps_pin",              "label": "GPS pin confirmation",     "required": true,  "show_when_accept_excess": null },
        { "key": "excess_cable_consent", "label": "Signed excess-cable consent", "required": true,  "show_when_accept_excess": true }
      ]
    }'::jsonb,
    TRUE,
    'M4 r2 default — mirrors the hardcoded round-1 list'
);

-- =====================================================================
-- 2. Lead: capture which schema (+ which sales_type at create time)
-- was used at lead creation. This is observability, not a hard FK so
-- a schema can be deleted/replaced without nulling history.
-- =====================================================================
ALTER TABLE crm.leads
    ADD COLUMN IF NOT EXISTS onboarding_schema_id UUID
        REFERENCES crm.onboarding_schemas(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS sales_type_at_create TEXT
        CHECK (sales_type_at_create IN ('broadband','enterprise','both'));

-- =====================================================================
-- 3. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('crm', 'schema.read',    'View onboarding schemas'),
    ('crm', 'schema.manage',  'Create/edit onboarding schemas (round 2: publish-only)'),
    ('crm', 'dashboard.read', 'View the sales dashboard')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: everything (already has crm.* — these flow through too)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'crm'
  AND p.action IN ('schema.read','schema.manage','dashboard.read')
ON CONFLICT DO NOTHING;

-- product_admin manages the schema (consistent with existing product mgmt).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'product_admin'
  AND p.module = 'crm'
  AND p.action IN ('schema.read','schema.manage')
ON CONFLICT DO NOTHING;

-- sales_manager + sales_rep see the dashboard + read schemas.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager','sales_rep')
  AND p.module = 'crm'
  AND p.action IN ('schema.read','dashboard.read')
ON CONFLICT DO NOTHING;

-- operations_admin: read both (visibility).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'crm'
  AND p.action IN ('schema.read','dashboard.read')
ON CONFLICT DO NOTHING;
