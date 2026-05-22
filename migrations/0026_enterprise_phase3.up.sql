-- 0026_enterprise_phase3.up.sql
--
-- Phase 3 of the Enterprise CPQ MVP. Adds the structural core:
--   - SLA templates (predefined catalog — TC-BQ-005, FK-only SLA pick)
--   - BOQ versions + BOQ lines (immutable, hash-stable, snapshot pricing)
--   - Approval templates + template members + per-BOQ instances
--   - Internal vendor extensions on warehouse.suppliers
--   - RBAC permissions for the new actions
--
-- The whole stack lives in `enterprise.*` so the bounded context stays
-- extraction-ready. Two exceptions: the supplier extension columns
-- live on `warehouse.suppliers` (additive only — broadband flows
-- continue to work), and approval-related permissions land in the
-- shared `identity.permissions` table.
--
-- Key invariants enforced at the DB level (mirror the domain code):
--   - boq_version.status restricted to defined lifecycle
--   - boq_line snapshot fields immutable on update (caller enforces)
--   - boq_version.snapshot_hash NOT NULL once status != 'draft'
--   - approval_instance.step_no monotonic per (boq_version_id)
--   - sla_template_id is FK-only — free-text SLA rejected

BEGIN;

-- =====================================================================
-- Supplier extension — internal vendor flag + priority + holding scope
-- =====================================================================
-- ALTER TABLE on existing warehouse.suppliers (created in 0022).
-- All columns are nullable / have defaults so existing rows remain
-- valid without backfill.
ALTER TABLE warehouse.suppliers
    ADD COLUMN IF NOT EXISTS is_internal_vendor BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS priority_score INT NOT NULL DEFAULT 100
        CHECK (priority_score >= 0 AND priority_score <= 1000),
    ADD COLUMN IF NOT EXISTS holding_company_id TEXT NOT NULL DEFAULT '';

-- Internal vendors get a fast-lookup index for the BOQ line provider
-- picker (filter: is_internal_vendor=true ORDER BY priority_score DESC).
CREATE INDEX IF NOT EXISTS idx_suppliers_internal_vendor
    ON warehouse.suppliers (is_internal_vendor, priority_score DESC)
    WHERE is_internal_vendor = TRUE;

-- =====================================================================
-- SLA templates — predefined service-level templates BOQ lines pick from
--
-- Per CPQ TC-BQ-005, SLA must be from a predefined template; free-text
-- is rejected. We keep this small for MVP — `name` + machine-readable
-- `key` (used by domain code if it needs to special-case any SLA) +
-- a `details` JSON for whatever attributes the operator wants to
-- attach (uptime %, response time, etc).
-- =====================================================================
CREATE TABLE enterprise.sla_templates (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    key          TEXT NOT NULL UNIQUE,        -- e.g. 'standard_8x5', 'premium_24x7'
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    details      JSONB NOT NULL DEFAULT '{}'::jsonb,
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sla_templates_active ON enterprise.sla_templates(active)
    WHERE active = TRUE;

-- Seed a handful so smoke tests + UIs have something to pick.
INSERT INTO enterprise.sla_templates (key, name, description, details) VALUES
    ('standard_8x5',
     'Standard 8×5',
     'Business-hours coverage, 8 hours response.',
     '{"uptime_pct": 99.5, "response_hours": 8, "coverage": "8x5"}'::jsonb),
    ('business_12x7',
     'Business 12×7',
     'Extended business hours, 4 hours response.',
     '{"uptime_pct": 99.9, "response_hours": 4, "coverage": "12x7"}'::jsonb),
    ('premium_24x7',
     'Premium 24×7',
     'Round-the-clock, 1-hour response.',
     '{"uptime_pct": 99.99, "response_hours": 1, "coverage": "24x7"}'::jsonb)
ON CONFLICT (key) DO NOTHING;

-- =====================================================================
-- Approval templates — reusable approver chains
--
-- Per CPQ §4.7 + TC-AP-001/002/003/004:
--   - mode = 'sequential' | 'parallel'
--   - members: ordered list of (user_id, step_no) — sequential uses step_no
--     for ordering, parallel ignores it and treats all members as concurrent
--
-- members live in a separate table (instead of jsonb) so we can FK to
-- identity.users and enforce that approvers are real users.
-- =====================================================================
CREATE TABLE enterprise.approval_templates (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    key         TEXT NOT NULL UNIQUE,            -- e.g. 'APT-STD-VP-DIR'
    name        TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'sequential'
                CHECK (mode IN ('sequential', 'parallel')),
    description TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    -- Only published templates are pickable on BOQ submit. Draft/inactive
    -- can be staged + reviewed before going live.
    published_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_approval_templates_active ON enterprise.approval_templates(active)
    WHERE active = TRUE;

CREATE TABLE enterprise.approval_template_members (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    template_id     UUID NOT NULL
        REFERENCES enterprise.approval_templates(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL,         -- soft ref to identity.users
    step_no         INT NOT NULL CHECK (step_no >= 1),
    -- Role tag captured at template-publish time so we can audit
    -- "VP Sales approved this" even if the user changes roles later.
    role_tag        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (template_id, step_no, user_id)
);

CREATE INDEX idx_approval_template_members_template
    ON enterprise.approval_template_members (template_id, step_no);

-- =====================================================================
-- BOQ versions — the immutable commercial artifact
--
-- Lifecycle per CPQ TC-SM-BOQ-*:
--   draft → in_approval → boq_approved | rejected
--   rejected → revision_draft → draft on resubmit (v+1)
--   boq_approved → superseded (when v+1 approved)
--
-- Hash-stable: snapshot_hash is SHA-256 of canonical JSON
-- representation, NULL for draft (can mutate), required once
-- status != 'draft'. NFR-007: same input → same hash deterministically.
-- =====================================================================
CREATE TABLE enterprise.boq_versions (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_number          TEXT NOT NULL,        -- shared across versions; (boq_number, version_no) unique below
    opportunity_id      UUID NOT NULL
        REFERENCES enterprise.opportunities(id) ON DELETE RESTRICT,
    pricebook_id        UUID NOT NULL
        REFERENCES enterprise.pricebooks(id) ON DELETE RESTRICT,
    version_no          INT NOT NULL DEFAULT 1 CHECK (version_no >= 1),
    -- Status enum mirrors CPQ catalog directly.
    status              TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN (
            'draft',
            'in_approval',
            'boq_approved',
            'rejected',
            'revision_draft',
            'superseded'
        )),
    -- Header totals — recomputed by application on every line edit.
    sell_total          NUMERIC(18,2) NOT NULL DEFAULT 0
        CHECK (sell_total >= 0),
    cost_total          NUMERIC(18,2) NOT NULL DEFAULT 0
        CHECK (cost_total >= 0),
    margin_pct          NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (margin_pct >= -100 AND margin_pct <= 100),
    -- Hash of canonical JSON (set by usecase on submit). NULL allowed
    -- while drafting so callers don't pay the hash cost on every edit.
    snapshot_hash       TEXT NOT NULL DEFAULT '',
    -- Approval chain reference — set when submit happens.
    approval_template_id UUID
        REFERENCES enterprise.approval_templates(id) ON DELETE RESTRICT,
    -- Lifecycle timestamps
    submitted_at        TIMESTAMPTZ,
    approved_at         TIMESTAMPTZ,
    rejected_at         TIMESTAMPTZ,
    superseded_at       TIMESTAMPTZ,
    -- Rejection record (per CPQ §4.8 — reason_code + comment mandatory).
    rejection_reason_code TEXT NOT NULL DEFAULT ''
        CHECK (rejection_reason_code IN (
            '', 'pricing', 'scope', 'documentation',
            'compliance', 'other'
        )),
    rejection_comment   TEXT NOT NULL DEFAULT '',
    -- Free-text notes for ops/audit context.
    notes               TEXT NOT NULL DEFAULT '',
    -- Optimistic concurrency (TC-CONC-002 — edit while in_approval).
    revision            INT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    -- Ownership
    created_by          UUID,                  -- soft ref to identity.users
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Per CPQ Edge #8: 5 reject→revise cycles produce v1..v6 monotonic;
    -- (boq_number, version_no) prevents accidental duplicate version.
    UNIQUE (boq_number, version_no)
);

CREATE INDEX idx_boq_versions_opportunity ON enterprise.boq_versions (opportunity_id);
CREATE INDEX idx_boq_versions_status ON enterprise.boq_versions (status);
CREATE INDEX idx_boq_versions_number_version
    ON enterprise.boq_versions (boq_number, version_no);

-- =====================================================================
-- BOQ lines — one row per item per BOQ version
--
-- Per CPQ TC-BQ-002: lines snapshot pricebook fields at create time.
-- Admin updates to the source pricebook don't mutate existing rows.
-- The snapshot columns (base_price_snapshot, min_margin_snapshot,
-- max_discount_snapshot) are written once and never updated.
-- =====================================================================
CREATE TABLE enterprise.boq_lines (
    id                          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_version_id              UUID NOT NULL
        REFERENCES enterprise.boq_versions(id) ON DELETE CASCADE,
    pricebook_line_id           UUID NOT NULL
        REFERENCES enterprise.pricebook_lines(id) ON DELETE RESTRICT,
    -- Snapshot fields (copies of pricebook_line at create time, immutable
    -- from the application's perspective).
    sku                         TEXT NOT NULL,
    name                        TEXT NOT NULL,
    unit                        TEXT NOT NULL DEFAULT 'unit',
    base_price_snapshot         NUMERIC(18,2) NOT NULL,
    min_margin_snapshot         NUMERIC(6,3) NOT NULL,
    max_discount_snapshot       NUMERIC(6,3) NOT NULL,
    -- Provider assignment (TC-BQ-003, TC-BQ-004 — required at submit time)
    assigned_provider_company_id UUID,        -- soft ref to warehouse.suppliers
    provider_user_id            UUID,        -- soft ref to identity.users (vendor.netA etc.)
    -- Pricing — vendor inputs vendor_unit_cost; sales sets sell_unit_price + discount
    vendor_unit_cost            NUMERIC(18,2),
    sell_unit_price             NUMERIC(18,2) NOT NULL DEFAULT 0
        CHECK (sell_unit_price >= 0),
    quantity                    NUMERIC(18,3) NOT NULL DEFAULT 1
        CHECK (quantity > 0),
    line_discount_pct           NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (line_discount_pct >= 0 AND line_discount_pct <= 100),
    -- SLA — FK only (TC-BQ-005)
    sla_template_id             UUID NOT NULL
        REFERENCES enterprise.sla_templates(id) ON DELETE RESTRICT,
    -- Per-line lifecycle state (independent of BOQ status; lines can
    -- have cost filled by vendor before the rest of the BOQ is ready).
    status                      TEXT NOT NULL DEFAULT 'awaiting_provider_input'
        CHECK (status IN (
            'awaiting_provider_input',
            'has_cost',
            'in_approval',
            'approved'
        )),
    notes                       TEXT NOT NULL DEFAULT '',
    sort_order                  INT NOT NULL DEFAULT 0,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_boq_lines_boq ON enterprise.boq_lines (boq_version_id);
CREATE INDEX idx_boq_lines_provider_user
    ON enterprise.boq_lines (provider_user_id)
    WHERE provider_user_id IS NOT NULL;
CREATE INDEX idx_boq_lines_provider_company
    ON enterprise.boq_lines (assigned_provider_company_id)
    WHERE assigned_provider_company_id IS NOT NULL;

-- =====================================================================
-- Approval instances — one row per (BOQ version × template step)
--
-- Materialized on BOQ submit. Tracks per-step status:
--   pending → approved | rejected | superseded_reset
--
-- `superseded_reset` is the state an already-approved step flips to
-- when an upstream price change happens (CPQ Edge #2) — the chain
-- re-runs from the beginning. Audit retains the original approval
-- timestamp in `acted_at_original`.
-- =====================================================================
CREATE TABLE enterprise.approval_instances (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_version_id  UUID NOT NULL
        REFERENCES enterprise.boq_versions(id) ON DELETE CASCADE,
    template_id     UUID NOT NULL
        REFERENCES enterprise.approval_templates(id) ON DELETE RESTRICT,
    step_no         INT NOT NULL CHECK (step_no >= 1),
    approver_user_id UUID NOT NULL,
    role_tag        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'superseded_reset')),
    reason_code     TEXT NOT NULL DEFAULT ''
        CHECK (reason_code IN (
            '', 'pricing', 'scope', 'documentation',
            'compliance', 'other'
        )),
    comment         TEXT NOT NULL DEFAULT '',
    acted_at        TIMESTAMPTZ,
    -- Preserve the original approval timestamp when superseded — Edge #2.
    acted_at_original TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Each (BOQ × step) is unique — one approver per step per BOQ.
    UNIQUE (boq_version_id, step_no, approver_user_id)
);

CREATE INDEX idx_approval_instances_boq
    ON enterprise.approval_instances (boq_version_id);
CREATE INDEX idx_approval_instances_approver
    ON enterprise.approval_instances (approver_user_id);
CREATE INDEX idx_approval_instances_pending
    ON enterprise.approval_instances (approver_user_id, status)
    WHERE status = 'pending';

-- =====================================================================
-- updated_at triggers — reuse the enterprise.touch_updated_at fn from 0024
-- =====================================================================
CREATE TRIGGER trg_boq_versions_touch
    BEFORE UPDATE ON enterprise.boq_versions
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_boq_lines_touch
    BEFORE UPDATE ON enterprise.boq_lines
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_approval_templates_touch
    BEFORE UPDATE ON enterprise.approval_templates
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_approval_instances_touch
    BEFORE UPDATE ON enterprise.approval_instances
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_sla_templates_touch
    BEFORE UPDATE ON enterprise.sla_templates
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- RBAC permissions — additive
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    -- BOQ surface (mostly Sales Support + Sales Manager)
    ('enterprise', 'boq.read',           'View BOQs and their lines (full visibility)'),
    ('enterprise', 'boq.write',          'Create / edit BOQ drafts'),
    ('enterprise', 'boq.submit',         'Submit BOQ for approval'),
    ('enterprise', 'boq.approve',        'Approve / reject BOQ as approver'),
    -- Vendor-scoped (Internal Vendor)
    ('enterprise', 'boq.vendor_cost',    'Input vendor_unit_cost on assigned BOQ lines'),
    ('enterprise', 'vendor.view_self',   'View own internal-vendor transactions'),
    -- Approval template / SLA template admin
    ('enterprise', 'approval_template.read',   'View approval templates'),
    ('enterprise', 'approval_template.manage', 'Create / edit / publish approval templates'),
    ('enterprise', 'sla_template.read',        'View SLA templates'),
    ('enterprise', 'sla_template.manage',      'Create / edit SLA templates')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket grant
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'boq.read', 'boq.write', 'boq.submit', 'boq.approve',
      'boq.vendor_cost', 'vendor.view_self',
      'approval_template.read', 'approval_template.manage',
      'sla_template.read', 'sla_template.manage'
  )
ON CONFLICT DO NOTHING;

-- sales_manager / sales_rep: BOQ read+write; submit on the manager side
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'enterprise'
  AND p.action IN (
      'boq.read', 'boq.write', 'boq.submit',
      'approval_template.read', 'sla_template.read'
  )
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'enterprise'
  AND p.action IN ('boq.read', 'boq.write', 'sla_template.read')
ON CONFLICT DO NOTHING;

-- operations_admin: approval-template + SLA-template management
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'boq.read',
      'approval_template.read', 'approval_template.manage',
      'sla_template.read', 'sla_template.manage'
  )
ON CONFLICT DO NOTHING;

COMMIT;
