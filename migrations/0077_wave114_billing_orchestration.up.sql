-- Wave 114 — Billing orchestration crons.
--
-- The five Wave 114 cron passes (reminder, late-fee, suspension,
-- restore-on-paid, commission-trigger) need durable per-attempt rows so
-- the ticks stay idempotent across restarts and so admin surfaces can
-- audit "what fired when." This migration adds four small append-only
-- log tables — one per orchestration pass except restore, which folds
-- into suspension_actions with action='restore'.
--
-- Permission seeds: admin-readable; cron writes via service code.
-- Down: drops the four tables.
--
-- Coordinates with parallel waves:
--   * Wave 111 (Payment Svc)  — migration 0074 (disjoint, internal/payment)
--   * Wave 112 (NOC mon)      — migration 0075 (disjoint, internal/nocmon)
--   * Wave 113 (NetDev Life)  — migration 0076 (disjoint, internal/netdevices)
--   * Wave 114 (this)         — migration 0077, only billing.* tables.

BEGIN;

-- =====================================================================
-- 1. billing.reminder_log — one row per (invoice, reminder_kind)
--
-- The reminder evaluator picks the next ReminderKind for an invoice
-- (soft_reminder → due_today → overdue_d1 → … → overdue_pre_suspend)
-- and writes one row per kind. The UNIQUE (invoice_id, kind) constraint
-- is the idempotency anchor: re-running the cron tick within the same
-- window cannot double-send the same reminder.
-- =====================================================================
CREATE TABLE IF NOT EXISTS billing.reminder_log (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id   UUID NOT NULL REFERENCES billing.invoices(id) ON DELETE RESTRICT,
    kind         TEXT NOT NULL CHECK (kind IN (
        'soft_reminder',
        'due_today',
        'overdue_d1',
        'overdue_d3',
        'overdue_d7',
        'overdue_pre_suspend'
    )),
    sent_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    channel      TEXT NOT NULL DEFAULT 'whatsapp',
    delivered    BOOLEAN,
    message_id   TEXT,
    error_msg    TEXT,
    UNIQUE (invoice_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_billing_reminder_log_invoice
    ON billing.reminder_log (invoice_id, sent_at DESC);

-- =====================================================================
-- 2. billing.late_fee_applications — one row per invoice (one charge)
--
-- The late-fee evaluator computes a single per-invoice fee when an
-- invoice crosses (due_date + grace_days). UNIQUE (invoice_id) means
-- subsequent re-runs no-op via INSERT … ON CONFLICT DO NOTHING. The
-- 'undo_at' field reserved for finance-side reversal (rare; outside
-- Wave 114's cron path).
-- =====================================================================
CREATE TABLE IF NOT EXISTS billing.late_fee_applications (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id         UUID NOT NULL REFERENCES billing.invoices(id) ON DELETE RESTRICT,
    schema_version_id  UUID,
    applied_amount     NUMERIC(18,2) NOT NULL,
    applied_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    basis              TEXT NOT NULL DEFAULT 'overdue',
    undo_at            TIMESTAMPTZ,
    undo_reason        TEXT,
    UNIQUE (invoice_id)
);
CREATE INDEX IF NOT EXISTS idx_billing_late_fee_apps_applied
    ON billing.late_fee_applications (applied_at DESC);

-- =====================================================================
-- 3. billing.suspension_actions — append-only state-change log
--
-- One row per ACTION the suspension evaluator (or the restore-on-paid
-- evaluator) takes for a customer. Distinct from the customer's
-- suspended_at on crm.customers — that's the latest state; this is
-- the full history. Actions: warn → soft_suspend → hard_suspend →
-- restore. The 'restore' action is written by the restore-on-paid
-- cron, NOT by the suspension evaluator. Both share the same table so
-- admin queries get a single ordered log.
-- =====================================================================
CREATE TABLE IF NOT EXISTS billing.suspension_actions (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id              UUID NOT NULL,
    triggered_by_invoice_id  UUID,
    schema_version_id        UUID,
    action                   TEXT NOT NULL CHECK (action IN (
        'warn',
        'soft_suspend',
        'hard_suspend',
        'restore'
    )),
    executed_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    grace_window_hours       INT,
    executed_by              TEXT NOT NULL DEFAULT 'cron'
);
CREATE INDEX IF NOT EXISTS idx_billing_suspension_actions_customer
    ON billing.suspension_actions (customer_id, executed_at DESC);
CREATE INDEX IF NOT EXISTS idx_billing_suspension_actions_kind
    ON billing.suspension_actions (action, executed_at DESC);

-- =====================================================================
-- 4. billing.commission_triggers — fired-trigger queue
--
-- The commission-trigger evaluator scans recently-paid invoices linked
-- to a plan_change_id with a sales_user_id and writes one row per
-- (plan_change_id, trigger_kind) the schema says should fire. This
-- table is the queue that a downstream worker (out of Wave 114's
-- scope) consumes into actual commission_records ledger rows.
--
-- UNIQUE (plan_change_id, trigger_kind) keeps the cron idempotent on
-- re-scan; a plan_change_id IS NULL is allowed for free-form triggers
-- (e.g. on_paid for non-plan-change invoices, which the evaluator can
-- choose to record at the customer level instead).
-- =====================================================================
CREATE TABLE IF NOT EXISTS billing.commission_triggers (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_change_id           UUID,
    customer_id              UUID,
    sales_user_id            UUID,
    trigger_kind             TEXT NOT NULL CHECK (trigger_kind IN (
        'on_paid',
        'on_activated',
        'on_anniversary',
        'manual'
    )),
    invoice_id               UUID,
    amount_basis             NUMERIC(18,2),
    schema_version_id        UUID,
    fired_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    commission_amount        NUMERIC(18,2),
    commission_recipient_id  UUID,
    UNIQUE (plan_change_id, trigger_kind)
);
CREATE INDEX IF NOT EXISTS idx_billing_commission_triggers_customer
    ON billing.commission_triggers (customer_id, sales_user_id);
CREATE INDEX IF NOT EXISTS idx_billing_commission_triggers_fired
    ON billing.commission_triggers (fired_at DESC);

-- =====================================================================
-- 5. Permission seeds (admin-readable; cron writes via service code)
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('billing', 'reminder.read',    'View reminder dispatch log'),
    ('billing', 'late_fee.read',    'View late-fee application log'),
    ('billing', 'suspension.read',  'View suspension action log'),
    ('billing', 'commission.read.triggers', 'View commission trigger queue')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin + finance roles read everything Wave 114 ships.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('super_admin','finance_admin','finance_manager')
  AND p.module = 'billing'
  AND p.action IN ('reminder.read','late_fee.read','suspension.read','commission.read.triggers')
ON CONFLICT DO NOTHING;

-- finance_staff: read all Wave 114 logs (no writes anywhere).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_staff'
  AND p.module = 'billing'
  AND p.action IN ('reminder.read','late_fee.read','suspension.read')
ON CONFLICT DO NOTHING;

-- operations_admin: suspension.read so ops can see who got cut & why.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'billing'
  AND p.action = 'suspension.read'
ON CONFLICT DO NOTHING;

COMMIT;
