-- 0024_enterprise_phase2.up.sql
--
-- Phase 2 of the Enterprise CPQ MVP rollout. Establishes the isolated
-- bounded context (`enterprise.*` schema) with three foundational
-- entities — Pricebook, Pricebook Line, and Opportunity — plus their
-- RBAC permission seeds.
--
-- Why a separate schema (vs. extending `crm.*`):
--   The enterprise bounded context is going to become its own service
--   (cmd/enterprise-svc). Keeping its tables in a dedicated schema
--   makes the future split trivial — no cross-schema FK constraints
--   to renegotiate, no naming collisions, and RBAC grants on the
--   service-DB-user can be scoped cleanly.
--
-- What's NOT in this migration:
--   - BOQ / BOQ lines (Phase 3)
--   - Internal vendor extensions on warehouse.suppliers (Phase 3)
--   - Quotation / Negotiation (Phase 4)
--   - Finance milestones / internal_transactions (Phase 5)
--   - EWO (Phase 5)
--
-- Cross-context references:
--   - opportunities.customer_id → crm.customers(id)
--   - opportunities.account_manager_user_id → identity.users(id)
--   - pricebooks.holding_company_id → for now NULL or string,
--     formalized when holding-company entity lands.

BEGIN;

CREATE SCHEMA IF NOT EXISTS enterprise;

-- =====================================================================
-- Pricebook header — versioned price catalog with effective windows.
--
-- Per CPQ TC-PB-001/PB-002, a pricebook has a `name`, `effective_from`,
-- `effective_to`, `currency`, and `holding_company_id`. Multiple
-- pricebooks can exist for the same scope, but their effective ranges
-- cannot overlap (TC-PB-002 — overlap returns HTTP 409 pricebook_overlap).
-- The overlap check is enforced at the application layer because Postgres
-- EXCLUDE constraints with daterange require btree_gist; we keep this
-- migration vanilla so it ports cleanly to the future enterprise-svc
-- whose connection user may not have the extension grant.
-- =====================================================================
CREATE TABLE enterprise.pricebooks (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code                TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    currency            CHAR(3) NOT NULL DEFAULT 'IDR'
        CHECK (currency = upper(currency)),
    effective_from      DATE NOT NULL,
    effective_to        DATE,                -- NULL = open-ended (current)
    holding_company_id  TEXT NOT NULL DEFAULT '',   -- future FK, string for now
    -- Versioning: every published pricebook is immutable. New publishes
    -- create a new row with version_no incremented; status moves through
    -- draft → published → superseded. Only one published row per `code`
    -- at a time — the partial unique index below enforces that.
    version_no          INT NOT NULL DEFAULT 1 CHECK (version_no >= 1),
    status              TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'superseded')),
    published_at        TIMESTAMPTZ,
    superseded_at       TIMESTAMPTZ,
    notes               TEXT NOT NULL DEFAULT '',
    created_by          UUID,        -- identity.users(id) — soft ref
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Only one "currently published" pricebook per code.
CREATE UNIQUE INDEX uq_pricebooks_published_per_code
    ON enterprise.pricebooks (code) WHERE status = 'published';

CREATE INDEX idx_pricebooks_status ON enterprise.pricebooks (status);
CREATE INDEX idx_pricebooks_holding ON enterprise.pricebooks (holding_company_id);

-- =====================================================================
-- Pricebook lines — one row per catalog item per pricebook version.
--
-- Per CPQ TC-PB-003/PB-004: lines carry `allowed_provider_company_ids`
-- (which internal vendors can supply this item), `owner_role`,
-- `base_price`, `default_margin_pct`, `min_margin_pct`, `max_discount_pct`.
-- Numeric guardrails (PB-O1):
--   - min_margin_pct must NOT exceed default_margin_pct
--   - max_discount_pct must be in [0, 100]
--   - base_price >= 0
-- Enforced in domain code (NewPricebookLine constructor) AND DB CHECK
-- so a manual SQL insert can't sneak past validation.
-- =====================================================================
CREATE TABLE enterprise.pricebook_lines (
    id                            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    pricebook_id                  UUID NOT NULL
        REFERENCES enterprise.pricebooks(id) ON DELETE CASCADE,
    sku                           TEXT NOT NULL,
    name                          TEXT NOT NULL,
    category                      TEXT NOT NULL DEFAULT '',
    description                   TEXT NOT NULL DEFAULT '',
    unit                          TEXT NOT NULL DEFAULT 'unit',
    -- Pricing (in pricebook.currency units)
    base_price                    NUMERIC(18,2) NOT NULL
        CHECK (base_price >= 0),
    default_margin_pct            NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (default_margin_pct >= 0 AND default_margin_pct <= 100),
    min_margin_pct                NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (min_margin_pct >= 0 AND min_margin_pct <= 100),
    max_discount_pct              NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (max_discount_pct >= 0 AND max_discount_pct <= 100),
    -- Provider whitelist — empty array means "any internal vendor"
    allowed_provider_company_ids  UUID[] NOT NULL DEFAULT '{}',
    -- Owner role hint — which role would normally manage this line
    -- (e.g. 'network_engineer', 'datacenter_admin'). Free string for
    -- now, formalized to enum if we need cross-validation later.
    owner_role                    TEXT NOT NULL DEFAULT '',
    -- BR-O1 paired constraint — min margin can never exceed default
    -- margin; otherwise auto-calc could never satisfy the floor.
    CONSTRAINT chk_min_le_default_margin
        CHECK (min_margin_pct <= default_margin_pct),
    sort_order                    INT NOT NULL DEFAULT 0,
    active                        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (pricebook_id, sku)
);

CREATE INDEX idx_pricebook_lines_pricebook ON enterprise.pricebook_lines (pricebook_id);
CREATE INDEX idx_pricebook_lines_sku ON enterprise.pricebook_lines (sku);

-- =====================================================================
-- Opportunity — the enterprise sales pipeline entity.
--
-- Mirrors the CPQ catalog's Opportunity (TC-OP-001 to TC-OP-009).
-- Lifecycle: Cold → Warm → Hot → Won (forward only). Lost is reachable
-- from any non-terminal stage but requires a reason (TC-OP-003).
-- Auto-Lost SLA (BR-9): Cold=30d, Warm=7d, Hot=3d since last activity;
-- the scheduler flips status='lost' with reason='stage_timeout' when
-- the window expires.
--
-- Pre-BOQ is stored as JSONB (TC-OP-005 — Pre-BOQ is a snapshot, NOT a
-- BOQ version). The actual immutable BOQ entity arrives in Phase 3.
-- =====================================================================
CREATE TABLE enterprise.opportunities (
    id                          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    opportunity_number          TEXT NOT NULL UNIQUE,
    -- Account / customer linkage
    customer_id                 UUID,       -- crm.customers(id) when convertible
    account_name                TEXT NOT NULL,           -- shown in pipeline before customer record exists
    account_industry            TEXT NOT NULL DEFAULT '',
    account_size                TEXT NOT NULL DEFAULT '',   -- 'sme'/'mid'/'enterprise'
    -- PIC (Person In Charge) at the account
    pic_name                    TEXT NOT NULL DEFAULT '',
    pic_title                   TEXT NOT NULL DEFAULT '',
    pic_phone                   TEXT NOT NULL DEFAULT '',
    pic_email                   TEXT NOT NULL DEFAULT '',
    -- Ownership
    owner_user_id               UUID,       -- account manager / sales rep
    branch_id                   UUID,       -- identity.branches(id)
    -- Pipeline stage — see lifecycle comment above
    stage                       TEXT NOT NULL DEFAULT 'cold'
        CHECK (stage IN ('cold', 'warm', 'hot', 'won', 'lost')),
    substage                    TEXT NOT NULL DEFAULT ''
        CHECK (substage IN ('', 'awaiting_po', 'po_validation', 'archived')),
    -- Commercial signals (filled in as the deal matures)
    estimated_value             NUMERIC(18,2) NOT NULL DEFAULT 0
        CHECK (estimated_value >= 0),
    currency                    CHAR(3) NOT NULL DEFAULT 'IDR',
    expected_close_at           DATE,
    -- Pricebook pin — once an Opportunity advances past Cold with a
    -- pricebook chosen, the version is locked here so downstream BOQs
    -- inherit the same prices even if Admin publishes a newer version.
    pricebook_id                UUID
        REFERENCES enterprise.pricebooks(id) ON DELETE RESTRICT,
    -- Source / referral
    source                      TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN (
            'manual','referral','cold_call','website','whatsapp',
            'social_media_dm','voip_call','line_call','walk_in',
            'event','partner','cs_referral'
        )),
    referrer_customer_id        UUID,        -- crm.customers(id)
    -- Pre-BOQ snapshot — JSON blob captured during Warm-stage qualification.
    -- Shape is intentionally loose (per TC-OP-005) so Sales can record
    -- whatever scope/requirements the customer agreed to. Hardening the
    -- shape is Phase 3 work alongside the formal BOQ.
    pre_boq                     JSONB NOT NULL DEFAULT '{}'::jsonb,
    pre_boq_completed_at        TIMESTAMPTZ,
    -- SLA tracking — used by the auto-Lost watchdog
    stage_entered_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_activity_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Lost record
    lost_reason_code            TEXT NOT NULL DEFAULT ''
        CHECK (lost_reason_code IN (
            '', 'price', 'feature_gap', 'unreachable', 'competitor',
            'project_cancelled', 'stage_timeout', 'other'
        )),
    lost_reason                 TEXT NOT NULL DEFAULT '',
    auto_lost                   BOOLEAN NOT NULL DEFAULT FALSE,
    -- Won record
    won_at                      TIMESTAMPTZ,
    po_reference                TEXT NOT NULL DEFAULT '',
    -- Notes (free text — owner's running journal)
    notes                       TEXT NOT NULL DEFAULT '',
    -- Optimistic concurrency control (TC-CONC-005: stale_version)
    revision                    INT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_opportunities_stage ON enterprise.opportunities (stage);
CREATE INDEX idx_opportunities_owner ON enterprise.opportunities (owner_user_id);
CREATE INDEX idx_opportunities_branch ON enterprise.opportunities (branch_id);
CREATE INDEX idx_opportunities_customer ON enterprise.opportunities (customer_id);
-- Composite for the auto-Lost scheduler — it scans non-terminal stages
-- ordered by stage_entered_at to find expired windows.
CREATE INDEX idx_opportunities_stage_entered
    ON enterprise.opportunities (stage, stage_entered_at)
    WHERE stage IN ('cold', 'warm', 'hot');

-- =====================================================================
-- updated_at trigger — keep the column honest without app-layer effort
-- =====================================================================
CREATE OR REPLACE FUNCTION enterprise.touch_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_pricebooks_touch
    BEFORE UPDATE ON enterprise.pricebooks
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_pricebook_lines_touch
    BEFORE UPDATE ON enterprise.pricebook_lines
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_opportunities_touch
    BEFORE UPDATE ON enterprise.opportunities
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- RBAC permissions — additive to identity.permissions
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'pricebook.read',       'View enterprise pricebooks'),
    ('enterprise', 'pricebook.manage',     'Create / edit / publish pricebooks'),
    ('enterprise', 'opportunity.read',     'View enterprise opportunities'),
    ('enterprise', 'opportunity.write',    'Create / edit own opportunities'),
    ('enterprise', 'opportunity.manage',   'Reassign / takeover any opportunity'),
    ('enterprise', 'opportunity.advance',  'Advance opportunity stage'),
    ('enterprise', 'module.read',          'See the Enterprise module in navigation')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket grant (matches the 0006 / 0022 pattern)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
ON CONFLICT DO NOTHING;

-- sales_manager: full pipeline visibility + management
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'enterprise'
  AND p.action IN (
      'opportunity.read','opportunity.write','opportunity.manage',
      'opportunity.advance','pricebook.read','module.read'
  )
ON CONFLICT DO NOTHING;

-- sales_rep: read + write own opportunities (the BE filters list by owner
-- on this permission level; "manage" is what unlocks cross-rep visibility)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'enterprise'
  AND p.action IN (
      'opportunity.read','opportunity.write','opportunity.advance',
      'pricebook.read','module.read'
  )
ON CONFLICT DO NOTHING;

-- operations_admin: pricebook management lives here (procurement owns prices).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'pricebook.read','pricebook.manage','opportunity.read','module.read'
  )
ON CONFLICT DO NOTHING;

COMMIT;
