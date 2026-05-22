-- 0022_suppliers.up.sql
--
-- Master supplier / vendor registry per CRM-Sales-Enterprise PRD §5.1.
-- Lives in the `warehouse` schema because suppliers are the source of
-- intake records (PO ref + distributor name already flow onto each
-- asset record during goods receipt). Enterprise vendor management
-- (RFQ, project linkage) in Phase 2 will reuse this same registry.
--
-- Fields mirror PRD §5.1's "Vendor record fields" verbatim:
--   - Company name (required)
--   - Contact person + phone + email
--   - Address
--   - Service categories supplied
--   - Payment terms (net_30, net_45, …)
--   - Active / Inactive status
--   - Onboarding date
--   - Documents: NPWP, NIB, business license, vendor agreement
--
-- `category_tags` is a Postgres array so an admin can tag a supplier
-- with multiple service categories without an N:M table — the cardinality
-- is small (a dozen tags per supplier at most) and we never need to
-- join the tag list to anything else, so a denormalized array is the
-- right primitive.

BEGIN;

CREATE TABLE warehouse.suppliers (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code            TEXT NOT NULL UNIQUE,
    company_name    TEXT NOT NULL,
    contact_person  TEXT NOT NULL DEFAULT '',
    phone           TEXT NOT NULL DEFAULT '',
    email           TEXT NOT NULL DEFAULT '',
    address         TEXT NOT NULL DEFAULT '',
    payment_terms   TEXT NOT NULL DEFAULT '',  -- e.g. "net_30", "net_45", "cod"
    npwp            TEXT NOT NULL DEFAULT '',  -- Indonesian tax ID
    nib             TEXT NOT NULL DEFAULT '',  -- Business identification number
    category_tags   TEXT[] NOT NULL DEFAULT '{}',
    notes           TEXT NOT NULL DEFAULT '',
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    onboarded_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index choices:
--   active first — the list view defaults to active-only, so this is
--   the hot path; partial index keeps inactive rows out of the b-tree.
--   company_name b-tree — supports the "search by name" filter on the
--   admin page; LIKE 'foo%' will hit the index.
CREATE INDEX idx_suppliers_active ON warehouse.suppliers(active) WHERE active = TRUE;
CREATE INDEX idx_suppliers_company_name ON warehouse.suppliers(company_name);

-- ----------------------------------------------------------------------
-- Permission seeds — mirror the existing warehouse pattern from 0006.
-- The route layer expects `warehouse.supplier.read` / `.manage`, which
-- maps to (module='warehouse', action='supplier.read'/'supplier.manage').
-- ----------------------------------------------------------------------
INSERT INTO identity.permissions (module, action, description) VALUES
    ('warehouse', 'supplier.read',   'View the supplier / vendor registry'),
    ('warehouse', 'supplier.manage', 'Create / edit / deactivate suppliers')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket grant (same pattern as 0006 — every new perm
-- becomes available to super_admin automatically).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'warehouse'
  AND p.action IN ('supplier.read', 'supplier.manage')
ON CONFLICT DO NOTHING;

-- operations_admin gets read+manage — procurement decisions cross
-- multiple branches, so this lives at the ops-admin level rather than
-- per-warehouse staff.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'warehouse'
  AND p.action IN ('supplier.read', 'supplier.manage')
ON CONFLICT DO NOTHING;

-- warehouse_manager: read-only — they reference suppliers when
-- recording goods receipts but don't onboard new ones. Adding a
-- supplier is an ops/finance decision (paper trail, vendor agreement).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('warehouse_manager', 'warehouse_staff')
  AND p.module = 'warehouse'
  AND p.action = 'supplier.read'
ON CONFLICT DO NOTHING;

COMMIT;
