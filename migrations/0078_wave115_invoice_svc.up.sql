-- Wave 115 — Invoice Service extraction + Add-On Billing.
--
-- Establishes a dedicated `invoicesvc` schema as the seam where a
-- standalone Invoice Service can extract from `internal/billing/`
-- without forcing a Big Bang. Three concerns land here:
--
--   1. invoice_snapshots — immutable per-issuance capture of the
--      billing schema (rules + template) + line items. Future schema
--      changes never retroactively alter what was billed. This is the
--      audit anchor for TC-IGE-002.
--
--   2. credit_notes — parent → credit-note state machine
--      (draft → issued → applied | voided). TC-IGE-008 parent-child
--      audit chain. Lives in invoicesvc so the read side can union
--      across billing.invoices + enterprise.invoices later.
--
--   3. bulk_generation_jobs + bulk_generation_items — async bulk
--      runs with per-customer item tracking (queued/generated/
--      failed/skipped). TC-IGE-007 100k-invoice / 30-min path; also
--      monthly_cycle reruns + correction passes.
--
-- Add-On Billing extension (additive in billing schema):
--
--   - billing.add_on_purchases — purchase ledger tracking
--     base-product add-ons. Status lifecycle:
--       pending_install → active → expired | cancelled
--     Wired off the existing customer-portal /portal/addons/buy
--     handler (CRM) so we don't duplicate. The new billing usecase
--     mirrors active add-ons for the billing read side + the cancel
--     flow.
--
-- Coordinates with parallel waves:
--   * Wave 116 (platform schema validators) — migration disjoint.
--   * Wave 117 (warehouse) — migration disjoint.
--
-- Permissions:
--   invoicesvc.snapshot.read
--   invoicesvc.credit_note.read / .write / .approve
--   invoicesvc.bulk.run / .read
--   billing.addon.read / .cancel  (new — purchase already on CRM)
--
-- Down: DROP SCHEMA invoicesvc CASCADE; DROP TABLE billing.add_on_purchases.

BEGIN;

CREATE SCHEMA IF NOT EXISTS invoicesvc;

-- =====================================================================
-- 1. invoicesvc.invoice_snapshots — immutable at-issuance capture
--
-- One row per (invoice, snapshotted_at). The snapshot pins the billing
-- schema version that was current AT issue time + the line items so
-- post-hoc schema edits don't change what the customer was billed.
-- =====================================================================
CREATE TABLE IF NOT EXISTS invoicesvc.invoice_snapshots (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id           UUID NOT NULL,
    customer_id          UUID,
    plan_id              UUID,
    schema_snapshot_id   UUID,
    snapshotted_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    total_amount         NUMERIC(18,2),
    line_items           JSONB NOT NULL,
    status_at_snapshot   TEXT,
    source_module        TEXT NOT NULL DEFAULT 'billing'
        CHECK (source_module IN ('billing', 'enterprise', 'manual')),
    UNIQUE (invoice_id, snapshotted_at)
);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_snap_customer
    ON invoicesvc.invoice_snapshots (customer_id, snapshotted_at DESC);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_snap_invoice
    ON invoicesvc.invoice_snapshots (invoice_id);

-- =====================================================================
-- 2. invoicesvc.credit_notes — issuer-driven credit lifecycle
--
-- Modeled after billing.invoices' state machine but kept under the
-- invoicesvc schema so the credit-note audit chain (TC-IGE-008) lives
-- independently of either parent invoice domain (billing or
-- enterprise). credit_no is the human-readable identifier (UNIQUE).
-- =====================================================================
CREATE TABLE IF NOT EXISTS invoicesvc.credit_notes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id   UUID NOT NULL,
    customer_id  UUID,
    credit_no    TEXT UNIQUE,
    amount       NUMERIC(18,2) NOT NULL CHECK (amount >= 0),
    reason       TEXT,
    status       TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'issued', 'applied', 'voided')),
    issued_at    TIMESTAMPTZ,
    applied_at   TIMESTAMPTZ,
    voided_at    TIMESTAMPTZ,
    created_by   UUID,
    approved_by  UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_cn_invoice
    ON invoicesvc.credit_notes (invoice_id);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_cn_status
    ON invoicesvc.credit_notes (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_cn_customer
    ON invoicesvc.credit_notes (customer_id);

-- =====================================================================
-- 3. invoicesvc.bulk_generation_jobs + items — async bulk run tracking
--
-- target_filter captures the queue's WHERE clause (e.g. cycle id,
-- branch list, customer-type filter). status follows
-- pending → running → completed | failed | partial, with totals updated
-- as items roll up. error_summary aggregates the per-item error_msg
-- counts so dashboards can render a one-row-per-job overview.
-- =====================================================================
CREATE TABLE IF NOT EXISTS invoicesvc.bulk_generation_jobs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind             TEXT NOT NULL CHECK (kind IN (
        'monthly_cycle',
        'add_on',
        'adjustment',
        'correction'
    )),
    target_filter    JSONB,
    status           TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'partial')),
    total_expected   INT,
    total_generated  INT DEFAULT 0,
    total_failed     INT DEFAULT 0,
    started_at       TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    error_summary    JSONB,
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_bulk_status
    ON invoicesvc.bulk_generation_jobs (status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_bulk_kind
    ON invoicesvc.bulk_generation_jobs (kind, started_at DESC);

CREATE TABLE IF NOT EXISTS invoicesvc.bulk_generation_items (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id         UUID NOT NULL REFERENCES invoicesvc.bulk_generation_jobs(id) ON DELETE CASCADE,
    customer_id    UUID,
    invoice_id     UUID,
    status         TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'generated', 'failed', 'skipped')),
    error_msg      TEXT,
    generated_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_invoicesvc_bulk_items_job
    ON invoicesvc.bulk_generation_items (job_id, status);

-- =====================================================================
-- 4. billing.add_on_purchases — billing-side ledger for add-on lifecycle
--
-- The /portal/addons/buy CRM handler already inserts crm.customer_addons.
-- This billing-side mirror exists so the recurring scheduler + the
-- billing read side can show add-on charges per cycle without going
-- cross-schema. The invoice_id link is populated by the addon usecase
-- when a one-time invoice is generated for the purchase.
-- =====================================================================
CREATE TABLE IF NOT EXISTS billing.add_on_purchases (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL,
    addon_sku       TEXT NOT NULL,
    addon_name      TEXT,
    category        TEXT CHECK (category IN ('digital', 'physical', 'service')),
    qty             INT NOT NULL DEFAULT 1 CHECK (qty > 0),
    unit_price      NUMERIC(18,2) NOT NULL CHECK (unit_price >= 0),
    total           NUMERIC(18,2) NOT NULL CHECK (total >= 0),
    invoice_id      UUID,
    status          TEXT NOT NULL DEFAULT 'pending_install'
        CHECK (status IN ('pending_install', 'active', 'expired', 'cancelled')),
    valid_from      TIMESTAMPTZ,
    valid_until     TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ,
    cancel_reason   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_billing_addon_customer
    ON billing.add_on_purchases (customer_id, status);
CREATE INDEX IF NOT EXISTS idx_billing_addon_invoice
    ON billing.add_on_purchases (invoice_id);
CREATE INDEX IF NOT EXISTS idx_billing_addon_active
    ON billing.add_on_purchases (status, valid_until);

-- =====================================================================
-- 5. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('invoicesvc', 'snapshot.read',        'View invoice snapshots'),
    ('invoicesvc', 'credit_note.read',     'View credit notes'),
    ('invoicesvc', 'credit_note.write',    'Create / void credit notes'),
    ('invoicesvc', 'credit_note.approve',  'Issue (approve) a credit note'),
    ('invoicesvc', 'bulk.run',             'Start bulk invoice generation'),
    ('invoicesvc', 'bulk.read',            'View bulk job + item status'),
    ('invoicesvc', 'monitoring.read',      'View invoice monitoring aggregations'),
    ('invoicesvc', 'monitoring.read.self', 'View own invoices (customer)'),
    ('billing',    'addon.read',           'View customer add-on purchases'),
    ('billing',    'addon.cancel',         'Cancel an active add-on (next-cycle)')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin + finance roles get the full invoicesvc read+write surface.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('super_admin', 'finance_admin', 'finance_manager')
  AND p.module = 'invoicesvc'
  AND p.action IN (
      'snapshot.read',
      'credit_note.read', 'credit_note.write', 'credit_note.approve',
      'bulk.run', 'bulk.read',
      'monitoring.read'
  )
ON CONFLICT DO NOTHING;

-- finance_staff: read-only across the invoicesvc surface.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_staff'
  AND p.module = 'invoicesvc'
  AND p.action IN ('snapshot.read', 'credit_note.read', 'bulk.read', 'monitoring.read')
ON CONFLICT DO NOTHING;

-- sales / sales_manager: read-only on monitoring (see own customers,
-- handler-side scoping applies).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales', 'sales_manager')
  AND p.module = 'invoicesvc'
  AND p.action = 'monitoring.read'
ON CONFLICT DO NOTHING;

-- Customer-self portal permission: monitoring.read.self.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('customer', 'customer_portal_user')
  AND p.module = 'invoicesvc'
  AND p.action = 'monitoring.read.self'
ON CONFLICT DO NOTHING;

-- Add-on billing permissions.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('super_admin', 'finance_admin', 'finance_manager', 'finance_staff', 'customer_service')
  AND p.module = 'billing'
  AND p.action IN ('addon.read', 'addon.cancel')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales', 'sales_manager')
  AND p.module = 'billing'
  AND p.action = 'addon.read'
ON CONFLICT DO NOTHING;

COMMIT;
