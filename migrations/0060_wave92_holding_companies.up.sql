-- Wave 92 — Multi-company holding foundation.
--
-- Phase 1 Enterprise compliance needs a multi-tenant data model that
-- threads holding-company + subsidiary identity through pricebook,
-- opportunity, BOQ, EWO, and so on. This migration lays the foundation:
-- two skeleton tables (`enterprise.holding_companies` and
-- `enterprise.subsidiaries`) plus a small demo seed so test/dev
-- environments boot with a usable tenant out of the box.
--
-- Intentionally NOT done in this wave:
--   - No foreign keys from existing enterprise tables (pricebooks,
--     opportunities, invoices, etc.) — those are added in a follow-up
--     wave once backfill rules are agreed. Adding FKs now would break
--     migrations on environments that already hold un-tenanted data.
--   - No RBAC scoping. The read permission is added so the FE can
--     gate the picker; row-level tenant scoping lands later.
--
-- Cross-context references:
--   - Subsidiaries carry an `is_pkp` flag + a per-tenant `ppn_rate`.
--     11% is the Indonesian default (DJP e-Faktur) — overrideable per
--     subsidiary when a tenant uses a different effective rate.
--   - `role` enumerates the subsidiary's posture in the holding:
--       reseller     → faces the customer, owns the contract
--       supplier     → procurement / vendor-side entity
--       holding_ops  → shared services (HR, finance, infra)

BEGIN;

CREATE TABLE enterprise.holding_companies (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name                TEXT NOT NULL,
    npwp                TEXT,
    legal_entity_type   TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE enterprise.subsidiaries (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    holding_company_id  UUID NOT NULL REFERENCES enterprise.holding_companies(id) ON DELETE RESTRICT,
    name                TEXT NOT NULL,
    npwp                TEXT,
    is_pkp              BOOLEAN NOT NULL DEFAULT FALSE,
    ppn_rate            NUMERIC(5, 4) NOT NULL DEFAULT 0.11,
    role                TEXT NOT NULL CHECK (role IN ('reseller', 'supplier', 'holding_ops')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_subsidiaries_holding ON enterprise.subsidiaries (holding_company_id);

-- touch updated_at on row updates — reuse the existing trigger fn from
-- 0024_enterprise_phase2 (enterprise.touch_updated_at).
CREATE TRIGGER trg_holding_companies_touch
    BEFORE UPDATE ON enterprise.holding_companies
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_subsidiaries_touch
    BEFORE UPDATE ON enterprise.subsidiaries
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- Demo seed — one holding + two subsidiaries (reseller + supplier)
-- so dev / test environments can boot and frontend dropdowns are not
-- empty. Fixed UUIDs so other seed scripts can reference them.
-- =====================================================================
INSERT INTO enterprise.holding_companies (id, name, npwp, legal_entity_type) VALUES
    ('00000000-0000-0000-0000-000000000092',
     'ION Network Holding',
     '01.234.567.8-901.000',
     'PT')
ON CONFLICT (id) DO NOTHING;

INSERT INTO enterprise.subsidiaries (id, holding_company_id, name, npwp, is_pkp, ppn_rate, role) VALUES
    ('00000000-0000-0000-0000-000000000093',
     '00000000-0000-0000-0000-000000000092',
     'ION Reseller Indonesia',
     '02.345.678.9-012.000',
     TRUE, 0.11, 'reseller'),
    ('00000000-0000-0000-0000-000000000094',
     '00000000-0000-0000-0000-000000000092',
     'ION Supplier Nusantara',
     '03.456.789.0-123.000',
     TRUE, 0.11, 'supplier')
ON CONFLICT (id) DO NOTHING;

-- =====================================================================
-- RBAC — single read permission for the picker. Manage / write rights
-- land with the follow-up wave that exposes mutation endpoints.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'holding_company.read', 'View holding companies and subsidiaries')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket grant (matches the 0006 / 0022 / 0024 pattern)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action = 'holding_company.read'
ON CONFLICT DO NOTHING;

-- sales / ops roles get the read so the holding-company picker
-- populates in Opportunity + Pricebook screens.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager', 'sales_rep', 'operations_admin')
  AND p.module = 'enterprise'
  AND p.action = 'holding_company.read'
ON CONFLICT DO NOTHING;

COMMIT;
