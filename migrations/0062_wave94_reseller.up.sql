-- Wave 94 — Reseller bounded context (Phase 1 Enterprise).
--
-- Scope: Reseller Onboarding (10 TCs), Reseller Platform (12 TCs),
-- Wholesale Supply (8 TCs). The bounded context lives in its own
-- schema `reseller` and never reaches into other schemas — cross-
-- context references (parent_subsidiary_id, supplier_subsidiary_id,
-- approved_by) are stored as plain UUIDs and resolved by the calling
-- service at display time. This keeps the future extraction into its
-- own service trivial.
--
-- Tenant model (PRD §Reseller Platform §3.2):
--   Each reseller_account is its own platform tenant. The reseller-
--   platform HTTP surface MUST scope every read/write by the resolved
--   reseller_account_id from the session token. Wholesale orders,
--   sessions, and any future per-tenant resource carry that FK so a
--   missing WHERE clause becomes a not-found rather than a leak.

CREATE SCHEMA IF NOT EXISTS reseller;

-- =====================================================================
-- reseller_accounts — onboarding + KYC + credit ledger
-- =====================================================================
--
-- status lifecycle:
--   pending_kyc → approved → suspended → terminated
--                        ↘ terminated (terminal from any non-terminal)
-- margin_pct: per-account default margin applied on wholesale orders
-- credit_limit + balance: prepaid wallet ledger. balance = sum of
-- top-ups − sum of consumed (post-fulfillment) wholesale order totals.
-- Both stay snapshotted on the row so the platform dashboard avoids
-- aggregating ledger lines on every page-load.
CREATE TABLE reseller.reseller_accounts (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    parent_subsidiary_id UUID,
    name                 TEXT NOT NULL,
    npwp                 TEXT,
    contact_email        TEXT,
    contact_phone        TEXT,
    status               TEXT NOT NULL DEFAULT 'pending_kyc'
        CHECK (status IN ('pending_kyc','approved','suspended','terminated')),
    margin_pct           NUMERIC(5,4) NOT NULL DEFAULT 0.10,
    credit_limit         NUMERIC(18,2) NOT NULL DEFAULT 0,
    balance              NUMERIC(18,2) NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at          TIMESTAMPTZ,
    approved_by          UUID
);

CREATE INDEX idx_reseller_accounts_parent_status
    ON reseller.reseller_accounts (parent_subsidiary_id, status);

-- =====================================================================
-- wholesale_skus — catalog of items resellers can purchase
-- =====================================================================
--
-- sku_code is globally unique (not per-supplier) so the same code can't
-- mean two different things across the network. supplier_subsidiary_id
-- is the holding-company entity that fulfills the order — left as a
-- plain UUID because the holding-company entity model lands in a later
-- migration.
CREATE TABLE reseller.wholesale_skus (
    id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    supplier_subsidiary_id UUID NOT NULL,
    name                  TEXT,
    sku_code              TEXT UNIQUE,
    unit_price            NUMERIC(18,2),
    unit                  TEXT NOT NULL DEFAULT 'unit',
    is_active             BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =====================================================================
-- wholesale_orders + wholesale_order_lines — purchase workflow
-- =====================================================================
--
-- status lifecycle:
--   draft → submitted → approved → fulfilled
--                   ↘ rejected
--                   ↘ cancelled
--   draft → cancelled (also allowed)
-- subtotal + total snapshotted on header so reporting doesn't re-sum
-- lines on every list page. The usecase recomputes these in the same
-- tx as line inserts; the postgres adapter never touches them outside
-- a tx so the snapshot can't drift.
CREATE TABLE reseller.wholesale_orders (
    id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id   UUID NOT NULL,
    supplier_subsidiary_id UUID NOT NULL,
    order_no              TEXT UNIQUE,
    status                TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','submitted','approved','rejected','fulfilled','cancelled')),
    subtotal              NUMERIC(18,2) NOT NULL DEFAULT 0,
    total                 NUMERIC(18,2) NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at           TIMESTAMPTZ,
    fulfilled_at          TIMESTAMPTZ
);

CREATE INDEX idx_wholesale_orders_reseller
    ON reseller.wholesale_orders (reseller_account_id, status);

CREATE TABLE reseller.wholesale_order_lines (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    order_id    UUID NOT NULL REFERENCES reseller.wholesale_orders(id) ON DELETE CASCADE,
    sku_id      UUID NOT NULL,
    qty         INTEGER NOT NULL CHECK (qty > 0),
    unit_price  NUMERIC(18,2),
    line_total  NUMERIC(18,2)
);

CREATE INDEX idx_wholesale_order_lines_order
    ON reseller.wholesale_order_lines (order_id);

-- =====================================================================
-- platform_sessions — reseller-platform auth (stub for OAuth/SSO)
-- =====================================================================
--
-- Tenant isolation note: every row carries reseller_account_id and
-- every read/write on the platform surface MUST filter by the resolved
-- tenant id from the session. The HTTP middleware loads the token →
-- account_id once per request and stashes it in the request context.
-- Session tokens are opaque uuid strings for now; real OAuth lands in
-- a later wave.
CREATE TABLE reseller.platform_sessions (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id UUID NOT NULL,
    session_token       TEXT UNIQUE,
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at        TIMESTAMPTZ
);

CREATE INDEX idx_platform_sessions_account
    ON reseller.platform_sessions (reseller_account_id);

-- =====================================================================
-- Demo seed — one approved reseller + one active SKU
-- =====================================================================
--
-- Lets the platform smoke test issue a session token and pull a non-
-- empty catalog without going through the full onboarding flow. The
-- fixed UUIDs are intentional so the seed is idempotent across env
-- resets.
INSERT INTO reseller.reseller_accounts
    (id, name, npwp, contact_email, status, margin_pct, credit_limit, balance, approved_at)
VALUES (
    '00000000-0000-0000-0000-000000009401',
    'Demo Reseller Co.',
    '00.000.000.0-000.000',
    'demo-reseller@example.com',
    'approved',
    0.1500,
    10000000,
    5000000,
    NOW()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO reseller.wholesale_skus
    (id, supplier_subsidiary_id, name, sku_code, unit_price, unit, is_active)
VALUES (
    '00000000-0000-0000-0000-000000009402',
    '00000000-0000-0000-0000-000000009400',
    'Wholesale 100 Mbps Trunk',
    'WS-TRUNK-100',
    750000.00,
    'month',
    TRUE
)
ON CONFLICT (sku_code) DO NOTHING;
