-- M6 Round 2 — Recurring billing + late fees + auto-suspend/restore +
-- auto-termination trigger + commission calculation.
--
-- Round-2 scope (this migration ships the schema; the service ships
-- the scheduler + commission engine):
--
--   * billing.billing_cycles  — one row per (customer, month) the
--     scheduler has generated. Idempotency anchor: re-running the
--     scheduler on the same day cannot double-issue.
--
--   * billing.policies        — singleton config row holding the late
--     fee + suspension thresholds. Kept as a table (not env) so ops
--     can update it without a redeploy.
--
--   * billing.commission_records — per-order, per-party split written
--     once when the order's OTC invoice flips to paid.
--
--   * crm.customers           — add suspension audit fields so the
--     watcher knows when to escalate to termination.
--
-- Deferred to round-3 (no schema):
--   * WhatsApp/email reminders + delivery audit (needs messaging vendor)
--   * Referral reward records
--   * Voluntary termination flow
--   * Faktur Pajak persistence (DJP integration)
--   * Xendit gateway transactions

-- =====================================================================
-- 1. billing.policies — singleton config
--
-- We use a single-row table (id=1 enforced via PRIMARY KEY (id) + a
-- CHECK) so the scheduler reads predictable values. Setters go through
-- an UPSERT in the service.
-- =====================================================================
CREATE TABLE billing.policies (
    id                          INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    -- Grace period before late fees apply (days past due_date).
    late_fee_grace_days         INT NOT NULL DEFAULT 3,
    -- Late fee amount in IDR added once when an invoice is N days overdue.
    -- A single-charge model is simpler than per-day; we can switch to
    -- compounding in round-3 if billing rules call for it.
    late_fee_amount             NUMERIC(15,2) NOT NULL DEFAULT 25000,
    -- Days past due_date after which an unpaid invoice triggers
    -- customer suspension. Must be ≥ grace_days.
    suspend_after_days          INT NOT NULL DEFAULT 14,
    -- Days a customer can be suspended before auto-termination fires
    -- per the PRD's Suspension Schema termination_trigger block.
    terminate_after_suspended_days INT NOT NULL DEFAULT 30,
    -- Notification fired N days before termination so the customer can
    -- still pay and cancel. Surfaced for round-3 messaging; the watcher
    -- writes a row to the (round-3) notifications table.
    notify_customer_days_before INT NOT NULL DEFAULT 7,
    updated_by                  UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed defaults so the scheduler can start immediately.
INSERT INTO billing.policies (id) VALUES (1) ON CONFLICT DO NOTHING;

-- =====================================================================
-- 2. billing.billing_cycles — recurring invoice generation log
--
-- One row per (customer, period_start). The scheduler refuses to
-- regenerate the same (customer, period) because of the unique
-- constraint, so safe to re-run the tick.
-- =====================================================================
CREATE TABLE billing.billing_cycles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    order_id        UUID NOT NULL REFERENCES crm.orders(id) ON DELETE CASCADE,
    period_start    DATE NOT NULL,
    period_end      DATE NOT NULL,
    invoice_id      UUID REFERENCES billing.invoices(id) ON DELETE SET NULL,
    -- 'generated' is the success state; 'skipped' tracks intentional
    -- skips (e.g. customer suspended at tick time).
    status          TEXT NOT NULL DEFAULT 'generated'
        CHECK (status IN ('generated','skipped','failed')),
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (customer_id, period_start)
);
CREATE INDEX idx_billing_cycles_period_start ON billing.billing_cycles (period_start DESC);
CREATE INDEX idx_billing_cycles_customer     ON billing.billing_cycles (customer_id, period_start DESC);

-- =====================================================================
-- 3. billing.commission_records — 5-party split on first payment
--
-- Per PRD M6: when the OTC invoice flips to paid, the system splits the
-- monthly_price into 5 buckets:
--   - sales_person       (the sales_id on the order)
--   - sales_manager      (walked via users.reports_to until role=sales_manager)
--   - sales_branch       (the branch the sales_person sits in)
--   - infrastructure_branch (the branch of the order's nearest_node — when cross-branch)
--   - company            (the residual)
-- Split percentages are configurable in round-3; round-2 uses a fixed
-- default that lives in the service as constants.
-- =====================================================================
CREATE TABLE billing.commission_records (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        UUID NOT NULL REFERENCES crm.orders(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    invoice_id      UUID REFERENCES billing.invoices(id) ON DELETE SET NULL,
    payment_id      UUID REFERENCES billing.payments(id) ON DELETE SET NULL,
    party_type      TEXT NOT NULL CHECK (party_type IN (
        'sales_person','sales_manager','sales_branch','infrastructure_branch','company'
    )),
    -- Each row points to whatever entity this party is — a user for
    -- person/manager, a branch for the *_branch types, NULL for company.
    user_id         UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    branch_id       UUID REFERENCES identity.branches(id) ON DELETE SET NULL,
    amount          NUMERIC(15,2) NOT NULL CHECK (amount >= 0),
    percentage      NUMERIC(5,2) NOT NULL CHECK (percentage >= 0 AND percentage <= 100),
    base_amount     NUMERIC(15,2) NOT NULL, -- monthly_price the split applied to
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (order_id, party_type)
);
CREATE INDEX idx_commission_user   ON billing.commission_records (user_id, created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX idx_commission_branch ON billing.commission_records (branch_id, created_at DESC) WHERE branch_id IS NOT NULL;

-- =====================================================================
-- 4. crm.customers — suspension audit fields
-- =====================================================================
ALTER TABLE crm.customers
    ADD COLUMN IF NOT EXISTS suspended_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suspend_reason TEXT,
    ADD COLUMN IF NOT EXISTS terminated_at TIMESTAMPTZ;

-- =====================================================================
-- 5. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('billing', 'cycles.read',     'View billing cycles + scheduler runs'),
    ('billing', 'cycles.run',      'Manually trigger a recurring-billing tick'),
    ('billing', 'policy.read',     'View billing policy (late fees, suspension thresholds)'),
    ('billing', 'policy.manage',   'Edit billing policy'),
    ('billing', 'commission.read', 'View commission records')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'billing'
  AND p.action IN ('cycles.read','cycles.run','policy.read','policy.manage','commission.read')
ON CONFLICT DO NOTHING;

-- finance roles get most surfaces; only super_admin runs the tick manually.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('finance_admin','finance_manager')
  AND p.module = 'billing'
  AND p.action IN ('cycles.read','policy.read','policy.manage','commission.read')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_staff'
  AND p.module = 'billing'
  AND p.action IN ('cycles.read','policy.read','commission.read')
ON CONFLICT DO NOTHING;

-- Sales rep can see their own commissions; PRD says "read-only commission
-- visibility in Sales App" — round-2 grants the read perm, the UI will
-- scope to their user_id (sales_manager sees their downline + own).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_rep','sales_manager')
  AND p.module = 'billing'
  AND p.action = 'commission.read'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'billing'
  AND p.action IN ('cycles.read','policy.read','commission.read')
ON CONFLICT DO NOTHING;
