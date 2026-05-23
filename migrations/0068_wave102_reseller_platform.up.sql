-- Wave 102 — Reseller Platform B2B2C subscriber CRUD + invoice inbox +
-- monthly submission UI backend. Extends the Wave 94 `reseller` schema.
--
-- Scope (PRD §Reseller Platform §3): the reseller-platform UI now ships
-- subscriber-CRUD + invoice-inbox + an MTD dashboard. Every new table
-- carries `reseller_account_id` and the platform HTTP surface scopes
-- every read/write by the resolved tenant. A missing WHERE clause
-- becomes a not-found rather than a cross-tenant leak — same contract
-- as Wave 94's wholesale_orders.
--
-- Lifecycle summary:
--   subscribers          : active ↔ suspended  →  terminated (terminal)
--   subscriber_invoices  : open → paid                       (terminal)
--                        : open → overdue → paid             (cron flip)
--                        : open|overdue → cancelled          (terminal)
--   subscriber_imports   : pending → processing → completed|partial|failed
--
-- Cross-context note: DashboardService reads the latest
-- partnership.compliance_evaluations row to surface the compliance
-- chip on the MTD dashboard. The read goes through a thin adapter
-- (internal/reseller/adapter/partnership/compliance_reader.go); no Go
-- imports cross the bounded-context line. If the partnership migration
-- (0066) hasn't applied, the adapter degrades to "unavailable".

BEGIN;

-- =====================================================================
-- reseller.subscribers — end-customer roster owned by one reseller
-- =====================================================================
--
-- Tenant isolation: every query MUST filter by reseller_account_id;
-- cross-tenant reads are forbidden. The id is the surrogate key
-- callers use everywhere; (reseller_account_id, id) is the natural
-- composite — keeping id global lets the API return a single uuid
-- in URLs without exposing the tenant.
--
-- status lifecycle (see domain/subscriber.go):
--   active ↔ suspended  →  terminated  (terminal)
-- The per-status timestamps are advisory — the state machine in the
-- domain layer is authoritative.
CREATE TABLE reseller.subscribers (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id UUID NOT NULL,
    customer_name       TEXT NOT NULL,
    customer_email      TEXT,
    customer_phone      TEXT,
    address_line        TEXT,
    sub_area_id         UUID,
    service_plan_id     UUID,
    monthly_fee         NUMERIC(18,2),
    status              TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','suspended','terminated')),
    notes               TEXT,
    activated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    suspended_at        TIMESTAMPTZ,
    terminated_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_reseller_subscribers_account_status
    ON reseller.subscribers (reseller_account_id, status);
CREATE INDEX idx_reseller_subscribers_account_created
    ON reseller.subscribers (reseller_account_id, created_at DESC);

-- =====================================================================
-- reseller.subscriber_invoices — invoice inbox per subscriber
-- =====================================================================
--
-- (reseller_account_id, invoice_no) is unique so a reseller can't
-- accidentally double-issue the same invoice number to two
-- subscribers. The composite is also the tenant-scoped lookup the
-- inbox uses; a missing reseller_account_id filter on the read path
-- short-circuits via the index rather than scanning the table.
--
-- period_year + period_month track which calendar period the invoice
-- covers; the invoice may be issued days/weeks after the period ends
-- so issued_at is independent. due_at drives the overdue evaluator.
CREATE TABLE reseller.subscriber_invoices (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id UUID NOT NULL,
    subscriber_id       UUID NOT NULL REFERENCES reseller.subscribers(id) ON DELETE RESTRICT,
    invoice_no          TEXT NOT NULL,
    period_year         INTEGER,
    period_month        INTEGER CHECK (period_month IS NULL OR period_month BETWEEN 1 AND 12),
    amount              NUMERIC(18,2),
    status              TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','paid','overdue','cancelled')),
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    due_at              TIMESTAMPTZ,
    paid_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (reseller_account_id, invoice_no)
);

CREATE INDEX idx_reseller_invoices_account_status_due
    ON reseller.subscriber_invoices (reseller_account_id, status, due_at);
CREATE INDEX idx_reseller_invoices_subscriber_period
    ON reseller.subscriber_invoices (subscriber_id, period_year DESC, period_month DESC);

-- =====================================================================
-- reseller.subscriber_imports — bulk-upload audit trail
-- =====================================================================
--
-- Each row records one CSV import attempt — total/ok/error counts +
-- error_summary jsonb (rows like {"row":5,"field":"monthly_fee","reason":"not numeric"}).
-- Status semantics:
--   pending     — accepted, work not started yet
--   processing  — usecase mid-flight
--   completed   — all rows persisted
--   partial     — some rows persisted, some failed validation
--   failed      — nothing persisted (e.g. header mismatch)
CREATE TABLE reseller.subscriber_imports (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id UUID NOT NULL,
    source              TEXT,
    total_rows          INTEGER NOT NULL DEFAULT 0,
    ok_rows             INTEGER NOT NULL DEFAULT 0,
    error_rows          INTEGER NOT NULL DEFAULT 0,
    raw_uploaded_url    TEXT,
    status              TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','processing','completed','failed','partial')),
    error_summary       JSONB,
    created_by          UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_reseller_subscriber_imports_account
    ON reseller.subscriber_imports (reseller_account_id, created_at DESC);

-- =====================================================================
-- Demo seed — 3 subscribers + 2 invoices for the Wave 94 demo reseller.
-- =====================================================================
--
-- The Wave 94 migration seeded reseller_account
-- 00000000-0000-0000-0000-000000009401. We give it three subscribers
-- (one terminated to exercise the lifecycle UI) plus two invoices
-- (open + paid) so the platform smoke test can hit the inbox without
-- a setup step. Fixed UUIDs keep the seed idempotent.
INSERT INTO reseller.subscribers
    (id, reseller_account_id, customer_name, customer_email, customer_phone,
     address_line, monthly_fee, status, activated_at)
VALUES
    ('00000000-0000-0000-0000-000000010201',
     '00000000-0000-0000-0000-000000009401',
     'Demo Subscriber A', 'sub-a@example.com', '+62-811-0001',
     'Jl. Demo No. 1', 350000.00, 'active', NOW()),
    ('00000000-0000-0000-0000-000000010202',
     '00000000-0000-0000-0000-000000009401',
     'Demo Subscriber B', 'sub-b@example.com', '+62-811-0002',
     'Jl. Demo No. 2', 500000.00, 'active', NOW()),
    ('00000000-0000-0000-0000-000000010203',
     '00000000-0000-0000-0000-000000009401',
     'Demo Subscriber C (terminated)', 'sub-c@example.com', '+62-811-0003',
     'Jl. Demo No. 3', 350000.00, 'terminated', NOW() - INTERVAL '60 days')
ON CONFLICT (id) DO NOTHING;

UPDATE reseller.subscribers
    SET terminated_at = NOW() - INTERVAL '30 days'
    WHERE id = '00000000-0000-0000-0000-000000010203' AND terminated_at IS NULL;

INSERT INTO reseller.subscriber_invoices
    (id, reseller_account_id, subscriber_id, invoice_no, period_year, period_month,
     amount, status, issued_at, due_at, paid_at)
VALUES
    ('00000000-0000-0000-0000-000000010204',
     '00000000-0000-0000-0000-000000009401',
     '00000000-0000-0000-0000-000000010201',
     'INV-DEMO-1001', 2026, 5,
     350000.00, 'open', NOW() - INTERVAL '5 days', NOW() + INTERVAL '10 days', NULL),
    ('00000000-0000-0000-0000-000000010205',
     '00000000-0000-0000-0000-000000009401',
     '00000000-0000-0000-0000-000000010202',
     'INV-DEMO-1002', 2026, 4,
     500000.00, 'paid', NOW() - INTERVAL '35 days', NOW() - INTERVAL '5 days', NOW() - INTERVAL '4 days')
ON CONFLICT (reseller_account_id, invoice_no) DO NOTHING;

-- =====================================================================
-- Permission catalog — 5 new permissions under module 'reseller'.
-- =====================================================================
--
-- Wave 94 didn't seed identity permissions for the reseller module
-- (the admin handler at the time relied on platform-only routes); we
-- backfill the subscriber/invoice/dashboard permissions here so the
-- new platform routes can be guarded with RequirePermission when (in
-- a later wave) the platform middleware is upgraded from raw session
-- tokens to JWT-with-permissions. Today the routes are still scoped
-- via TenantScope; the permissions exist for forward compatibility +
-- super_admin grant.
INSERT INTO identity.permissions (module, action, description) VALUES
    ('reseller', 'subscriber.read',  'View B2B2C subscribers under a reseller'),
    ('reseller', 'subscriber.write', 'Create/edit/suspend/terminate B2B2C subscribers'),
    ('reseller', 'invoice.read',     'View subscriber invoice inbox'),
    ('reseller', 'invoice.write',    'Mark invoices paid / cancelled'),
    ('reseller', 'dashboard.read',   'View reseller MTD dashboard (subscribers, invoices, compliance)')
ON CONFLICT (module, action) DO NOTHING;

-- =====================================================================
-- Role catalog — add reseller_admin if missing.
-- =====================================================================
INSERT INTO identity.roles (name, description) VALUES
    ('reseller_admin', 'Reseller-side admin: subscriber CRUD, invoice inbox, dashboard')
ON CONFLICT (name) DO NOTHING;

-- =====================================================================
-- Role → permission grants.
-- =====================================================================

-- super_admin gets every new reseller permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'reseller'
  AND p.action IN ('subscriber.read','subscriber.write','invoice.read','invoice.write','dashboard.read')
ON CONFLICT DO NOTHING;

-- reseller_admin gets subscriber.* + invoice.* + dashboard.read.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'reseller_admin'
  AND p.module = 'reseller'
  AND p.action IN ('subscriber.read','subscriber.write','invoice.read','invoice.write','dashboard.read')
ON CONFLICT DO NOTHING;

COMMIT;
