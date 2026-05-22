-- 0032_internal_foundation.up.sql
--
-- Foundation tables that don't depend on any 3rd party integration:
--   - internal_transactions (Enterprise sub-P&L per BOQ approval)
--   - schema_definitions + customer_schema_overrides (Schema System v1)
--   - ewo_checklist_templates (admin-reusable checklists)
--   - notification_preferences (per-user mute toggles)
--   - BOQ.rfq_id backlink + soft FK
--
-- Audit log table already exists in migration 0001 (identity.audit_logs).
-- This migration is purely additive — no breaking changes.

BEGIN;

-- platform schema holds cross-module rule definitions that any service
-- can read. Keeping schemas isolated from individual module schemas
-- (billing.*, crm.*, etc.) so they're trivially discoverable and not
-- tied to a service binary.
CREATE SCHEMA IF NOT EXISTS platform;

CREATE OR REPLACE FUNCTION platform.touch_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- =====================================================================
-- internal_transactions — sub-company revenue ledger
-- =====================================================================
-- Recorded when a BOQ flips to boq_approved: one row per line with a
-- vendor_unit_cost > 0, capturing the per-line sell vs cost. The
-- aggregate of (sell - cost) per BOQ rolls up the gross margin recognized
-- across internal vendors. This is the audit trail PRD §7.3 requires
-- for subsidiary P&L.

CREATE TABLE IF NOT EXISTS enterprise.internal_transactions (
    id                          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_version_id              UUID NOT NULL,
    boq_line_id                 UUID NOT NULL,
    quotation_id                UUID,  -- nullable: BOQ approval may pre-date quote
    -- The two sides of the transaction: internal vendor company supplies,
    -- the (parent) sales entity bills the customer. Both are soft refs to
    -- warehouse.suppliers / cross-context companies.
    vendor_company_id           UUID,
    sell_amount                 NUMERIC(18,2) NOT NULL,
    cost_amount                 NUMERIC(18,2) NOT NULL,
    margin_amount               NUMERIC(18,2) GENERATED ALWAYS AS (sell_amount - cost_amount) STORED,
    currency                    TEXT NOT NULL DEFAULT 'IDR',
    recognized_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notes                       TEXT NOT NULL DEFAULT '',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Same line should only generate one transaction row per approval — if
-- a BOQ is re-approved after revision, the prior row is preserved (it
-- belongs to a different boq_version_id) and a new row gets minted.
CREATE UNIQUE INDEX IF NOT EXISTS uq_internal_transactions_line
    ON enterprise.internal_transactions(boq_line_id);
CREATE INDEX IF NOT EXISTS idx_internal_transactions_boq
    ON enterprise.internal_transactions(boq_version_id, recognized_at DESC);
CREATE INDEX IF NOT EXISTS idx_internal_transactions_vendor
    ON enterprise.internal_transactions(vendor_company_id, recognized_at DESC);

-- =====================================================================
-- Schema System v1 — schema_definitions + customer_schema_overrides
-- =====================================================================
-- Versioned per-tenant rule sets that drive billing, commission, and
-- suspension behavior. One row per (kind, version_no). Customer-level
-- overrides patch specific fields on a published schema for that one
-- customer without forking the whole schema.
--
-- We deliberately re-use the `enterprise.boq_versions` versioning idiom
-- — published rows are immutable; drafts can iterate; only published
-- rows are pickable on customer assignment.

CREATE TYPE platform.schema_kind AS ENUM (
    'billing',
    'commission',
    'suspension',
    'service'
);

CREATE TABLE IF NOT EXISTS platform.schema_definitions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    kind            platform.schema_kind NOT NULL,
    code            TEXT NOT NULL,
    version_no      INT NOT NULL DEFAULT 1,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    -- Schema body — JSONB so the structure can evolve per kind without
    -- migrations. Examples:
    --   billing:   { "grace_days": 10, "late_fee_pct": 5, "suspend_after_days": 14, ... }
    --   commission:{ "rep_pct": 0.4, "mgr_pct": 0.1, "branch_pct": 0.05, ... }
    --   suspension:{ "reminder_hours_before": [72, 24], "grace_minutes_after_suspend": 60, ... }
    --   service:   { "speed_mbps": 50, "fup_gb": null, "static_ip": false, ... }
    body            JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'superseded')),
    published_at    TIMESTAMPTZ,
    superseded_at   TIMESTAMPTZ,
    notes           TEXT NOT NULL DEFAULT '',
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (kind, code, version_no)
);

CREATE INDEX IF NOT EXISTS idx_schema_definitions_status
    ON platform.schema_definitions(kind, status, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_schema_definitions_code
    ON platform.schema_definitions(kind, code);

CREATE TRIGGER trg_schema_definitions_touch
    BEFORE UPDATE ON platform.schema_definitions
    FOR EACH ROW EXECUTE FUNCTION platform.touch_updated_at();

-- Customer-level overrides — a thin patch on a published schema for one
-- customer. The `patch` jsonb is shallow-merged over the schema body at
-- evaluation time. The (customer_id, schema_kind) pair is unique — one
-- override per kind per customer.
CREATE TABLE IF NOT EXISTS platform.customer_schema_overrides (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id         UUID NOT NULL,
    schema_kind         platform.schema_kind NOT NULL,
    -- Pin to a specific schema version; nullable if the customer should
    -- always track the latest published version of `schema_code`.
    schema_id           UUID REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    schema_code         TEXT NOT NULL,
    patch               JSONB NOT NULL DEFAULT '{}'::jsonb,
    reason              TEXT NOT NULL DEFAULT '',
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until         TIMESTAMPTZ,
    revision            INT NOT NULL DEFAULT 1,
    created_by          UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (customer_id, schema_kind)
);

CREATE INDEX IF NOT EXISTS idx_customer_schema_overrides_customer
    ON platform.customer_schema_overrides(customer_id);
CREATE INDEX IF NOT EXISTS idx_customer_schema_overrides_kind
    ON platform.customer_schema_overrides(schema_kind, schema_code);

CREATE TRIGGER trg_customer_schema_overrides_touch
    BEFORE UPDATE ON platform.customer_schema_overrides
    FOR EACH ROW EXECUTE FUNCTION platform.touch_updated_at();

-- =====================================================================
-- EWO checklist templates — admin-managed reusable seed lists
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.ewo_checklist_templates (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    -- Items stored as jsonb so admins can edit the whole list atomically;
    -- shape: [{ seq_no, label, description }]
    items           JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ewo_checklist_templates_active
    ON enterprise.ewo_checklist_templates(active) WHERE active = TRUE;

-- =====================================================================
-- Notification preferences — per-user / per-kind mute toggles
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.notification_preferences (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL,
    kind            TEXT NOT NULL,  -- e.g. "boq.approval_pending", "*", "boq.*"
    -- Default ON; admin/op can set it off to mute that kind.
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_notif_prefs_user
    ON enterprise.notification_preferences(user_id);

-- =====================================================================
-- BOQ ↔ RFQ backlink — soft FK, no constraint
-- =====================================================================
-- When an RFQ is fulfilled by a BOQ, we currently only set
-- rfqs.fulfilled_boq_id. The reverse pointer here lets BOQ detail
-- pages render a "← fulfilling RFQ-...".

ALTER TABLE enterprise.boq_versions
    ADD COLUMN IF NOT EXISTS source_rfq_id UUID;

CREATE INDEX IF NOT EXISTS idx_boq_versions_source_rfq
    ON enterprise.boq_versions(source_rfq_id);

-- =====================================================================
-- New permissions for the schema system + admin surfaces
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('platform', 'schema.read',    'View platform schema definitions'),
    ('platform', 'schema.manage',  'Create / publish / supersede schema definitions'),
    ('platform', 'schema_override.read',   'View per-customer schema overrides'),
    ('platform', 'schema_override.manage', 'Apply / edit per-customer schema overrides'),
    ('enterprise', 'ewo_checklist_template.read',    'View EWO checklist templates'),
    ('enterprise', 'ewo_checklist_template.manage',  'Manage EWO checklist templates'),
    ('enterprise', 'notification_pref.manage', 'Edit own notification preferences'),
    ('enterprise', 'internal_transaction.read', 'View internal vendor revenue ledger')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin gets blanket access.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND (
    (p.module = 'platform'   AND p.action IN ('schema.read','schema.manage','schema_override.read','schema_override.manage'))
    OR (p.module = 'enterprise' AND p.action IN ('ewo_checklist_template.read','ewo_checklist_template.manage','notification_pref.manage','internal_transaction.read'))
  )
ON CONFLICT DO NOTHING;

-- finance reads schemas + transactions, manages billing schema.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance'
  AND (
    (p.module = 'platform' AND p.action IN ('schema.read','schema.manage','schema_override.read','schema_override.manage'))
    OR (p.module = 'enterprise' AND p.action IN ('internal_transaction.read','notification_pref.manage'))
  )
ON CONFLICT DO NOTHING;

-- sales_manager + sales_rep get template read + own notification prefs.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager', 'sales_rep')
  AND p.module = 'enterprise'
  AND p.action IN ('ewo_checklist_template.read', 'notification_pref.manage')
ON CONFLICT DO NOTHING;

COMMIT;
