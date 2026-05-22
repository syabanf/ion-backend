-- M6 Round 1 — Billing skeleton.
--
-- Round-1 scope: invoices (otc + recurring + excess_cable + addon),
-- invoice line items, payments (manual mark-paid; Xendit webhook stub
-- ready). Payment confirmation flips invoice status to 'paid' and
-- timestamps paid_at. The cross-cutting rule lands in M5's BAST verify:
-- approval requires the order's OTC invoice to be 'paid'.
--
-- Deferred to round 2:
--   - billing_cycles (recurring/anniversary scheduling)
--   - faktur_pajak (DJP e-Faktur)
--   - credit_notes (refund + adjustment)
--   - Xendit webhook receiver + signature verification
--   - WhatsApp Meta + email reminders
--   - Late-fee policy + auto-suspend/restore + auto-termination
--   - Commission calculation on first payment

CREATE SCHEMA IF NOT EXISTS billing;

-- =====================================================================
-- 1. Invoices
--
-- order_id is a soft FK to crm.orders so billing-svc stays standalone:
-- when CRM splits to its own process, billing only needs the ID, not the
-- row, and reads the rest through the CRM gateway. We DO hard-FK
-- customer_id since the customer is essentially the billing party (and
-- crm.customers lives in the same DB in Phase 1).
-- =====================================================================
CREATE TABLE billing.invoices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_number  TEXT NOT NULL UNIQUE,
    customer_id     UUID NOT NULL REFERENCES crm.customers(id) ON DELETE RESTRICT,
    order_id        UUID, -- soft FK to crm.orders
    invoice_type    TEXT NOT NULL CHECK (invoice_type IN ('otc','recurring','excess_cable','addon','milestone')),
    invoice_date    DATE NOT NULL,
    due_date        DATE NOT NULL,
    subtotal        NUMERIC(15,2) NOT NULL CHECK (subtotal >= 0),
    ppn_rate        NUMERIC(5,2) NOT NULL DEFAULT 11.00,
    ppn_amount      NUMERIC(15,2) NOT NULL CHECK (ppn_amount >= 0),
    total           NUMERIC(15,2) NOT NULL CHECK (total >= 0),
    status          TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','issued','paid','overdue','cancelled')),
    paid_at         TIMESTAMPTZ,
    notes           TEXT,
    created_by      UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_invoices_customer ON billing.invoices (customer_id);
CREATE INDEX idx_invoices_order    ON billing.invoices (order_id);
CREATE INDEX idx_invoices_status   ON billing.invoices (status);
CREATE INDEX idx_invoices_due_date ON billing.invoices (due_date);

-- One OTC invoice per order (round-1 invariant). Recurring will use
-- (order_id, cycle_id) once cycles land.
CREATE UNIQUE INDEX uniq_otc_per_order
    ON billing.invoices (order_id)
    WHERE invoice_type = 'otc' AND order_id IS NOT NULL;

-- =====================================================================
-- 2. Invoice line items
-- =====================================================================
CREATE TABLE billing.invoice_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id      UUID NOT NULL REFERENCES billing.invoices(id) ON DELETE CASCADE,
    line_order      INT NOT NULL DEFAULT 1,
    description     TEXT NOT NULL,
    item_type       TEXT NOT NULL CHECK (item_type IN ('mrc','otc','excess_cable','addon','penalty','credit','milestone')),
    quantity        NUMERIC(10,3) NOT NULL DEFAULT 1 CHECK (quantity > 0),
    unit_price      NUMERIC(15,2) NOT NULL,
    amount          NUMERIC(15,2) NOT NULL
);
CREATE INDEX idx_invoice_items_invoice ON billing.invoice_items (invoice_id);

-- =====================================================================
-- 3. Payments
--
-- payment_method is open text in round 1 (manual cash/transfer + Xendit
-- gateway tokens later). gateway_transaction_id is reserved for Xendit
-- webhook idempotency in round 2.
-- =====================================================================
CREATE TABLE billing.payments (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id             UUID NOT NULL REFERENCES billing.invoices(id) ON DELETE RESTRICT,
    customer_id            UUID NOT NULL REFERENCES crm.customers(id) ON DELETE RESTRICT,
    amount                 NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    payment_method         TEXT NOT NULL,
    gateway_transaction_id TEXT,
    payment_date           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_by           UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    status                 TEXT NOT NULL DEFAULT 'confirmed'
        CHECK (status IN ('pending','confirmed','failed','refunded')),
    notes                  TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_payments_invoice  ON billing.payments (invoice_id);
CREATE INDEX idx_payments_customer ON billing.payments (customer_id);
CREATE INDEX idx_payments_status   ON billing.payments (status);

-- Round-1 invariant: gateway_transaction_id is unique when present so
-- replayed webhooks don't double-pay. Manual payments have NULL gw_txn.
CREATE UNIQUE INDEX uniq_payments_gw_txn
    ON billing.payments (gateway_transaction_id)
    WHERE gateway_transaction_id IS NOT NULL;

-- =====================================================================
-- 4. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('billing', 'invoice.read',    'View invoices and payments'),
    ('billing', 'invoice.create',  'Create invoices manually'),
    ('billing', 'invoice.void',    'Void / cancel an invoice'),
    ('billing', 'payment.record',  'Record a confirmed payment manually')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: everything
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin' AND p.module = 'billing'
ON CONFLICT DO NOTHING;

-- finance_admin / finance_manager: everything
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('finance_admin','finance_manager')
  AND p.module = 'billing'
ON CONFLICT DO NOTHING;

-- finance_staff: read + payment.record (no void)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_staff'
  AND p.module = 'billing'
  AND p.action IN ('invoice.read','payment.record')
ON CONFLICT DO NOTHING;

-- operations_admin: read-only for visibility
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'billing'
  AND p.action = 'invoice.read'
ON CONFLICT DO NOTHING;

-- noc / noc_manager: invoice.read so the BAST verify queue can show
-- the payment gate status next to each pending BAST.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('noc','noc_manager')
  AND p.module = 'billing'
  AND p.action = 'invoice.read'
ON CONFLICT DO NOTHING;
