-- 0029_enterprise_finance.up.sql
--
-- Phase 5 — Finance + EWO (Engineering Work Order).
--
-- Three tables join the enterprise.* schema:
--
--   invoices         — issued from an accepted quotation. Header-only;
--                       totals are denormalized from the quote snapshot
--                       at issue time so a later quotation revision
--                       doesn't mutate historical billing.
--   invoice_payments — append-only ledger of payments against an invoice.
--                       Running balance is computed (server side) on
--                       insert; the invoice header carries paid_amount
--                       as the cached aggregate so list views avoid the
--                       sum() round trip.
--   ewos             — fulfillment handoff. Created (one per accepted
--                       quotation, idempotent on quotation_id) so the
--                       field/network teams can start delivery against
--                       a confirmed scope.
--
-- Cross-context note: invoice payments are intentionally NOT pushed to
-- the broadband billing schema. Enterprise CPQ owns its own invoicing
-- because the per-deal cadence + manual reconciliation differs from
-- the broadband monthly cycle. A future integration could mirror these
-- into the broadband ledger but that's out of MVP scope.

BEGIN;

-- =====================================================================
-- invoices
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.invoices (
    id                  UUID PRIMARY KEY,
    invoice_number      TEXT NOT NULL,
    quotation_id        UUID NOT NULL,
    opportunity_id      UUID NOT NULL,
    boq_version_id      UUID NOT NULL,
    -- Status state machine:
    --   draft    — created but not yet issued to customer (rare; we
    --              auto-flip to issued on creation for MVP)
    --   issued   — sent; awaiting payment
    --   partial  — payment(s) recorded but balance > 0
    --   paid     — balance == 0
    --   voided   — cancelled with a reason; immutable
    status              TEXT NOT NULL DEFAULT 'issued'
        CHECK (status IN ('draft', 'issued', 'partial', 'paid', 'voided')),
    total_amount        NUMERIC(18,2) NOT NULL,
    paid_amount         NUMERIC(18,2) NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'IDR',
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    due_at              TIMESTAMPTZ NOT NULL,
    paid_at             TIMESTAMPTZ,
    voided_at           TIMESTAMPTZ,
    void_reason         TEXT,
    notes               TEXT NOT NULL DEFAULT '',
    issued_by           UUID,
    revision            INT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT invoices_paid_amount_nonneg CHECK (paid_amount >= 0),
    CONSTRAINT invoices_paid_amount_capped CHECK (paid_amount <= total_amount)
);

-- One invoice per accepted quotation (TC-IN-001 idempotency).
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoices_quotation
    ON enterprise.invoices(quotation_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoices_invoice_number
    ON enterprise.invoices(invoice_number);
CREATE INDEX IF NOT EXISTS idx_invoices_opp ON enterprise.invoices(opportunity_id);
CREATE INDEX IF NOT EXISTS idx_invoices_status ON enterprise.invoices(status);
CREATE INDEX IF NOT EXISTS idx_invoices_due ON enterprise.invoices(due_at);

CREATE TRIGGER trg_invoices_touch
    BEFORE UPDATE ON enterprise.invoices
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- invoice_payments
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.invoice_payments (
    id              UUID PRIMARY KEY,
    invoice_id      UUID NOT NULL
        REFERENCES enterprise.invoices(id) ON DELETE RESTRICT,
    amount          NUMERIC(18,2) NOT NULL CHECK (amount > 0),
    method          TEXT NOT NULL
        CHECK (method IN ('bank_transfer', 'cash', 'check', 'card', 'other')),
    reference       TEXT NOT NULL DEFAULT '',
    paid_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notes           TEXT NOT NULL DEFAULT '',
    recorded_by     UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_invoice_payments_invoice
    ON enterprise.invoice_payments(invoice_id, paid_at DESC);

-- =====================================================================
-- ewos (engineering work orders)
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.ewos (
    id                  UUID PRIMARY KEY,
    ewo_number          TEXT NOT NULL,
    quotation_id        UUID NOT NULL,
    opportunity_id      UUID NOT NULL,
    boq_version_id      UUID NOT NULL,
    -- Status state machine:
    --   pending     — created, awaiting fulfillment team pickup
    --   in_progress — work has started
    --   completed   — delivered + accepted by the customer
    --   cancelled   — abandoned (with reason)
    status              TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'completed', 'cancelled')),
    assigned_to         UUID,
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    cancel_reason       TEXT NOT NULL DEFAULT '',
    notes               TEXT NOT NULL DEFAULT '',
    revision            INT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One EWO per accepted quotation (TC-EWO-001 idempotency).
CREATE UNIQUE INDEX IF NOT EXISTS uq_ewos_quotation
    ON enterprise.ewos(quotation_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ewos_number ON enterprise.ewos(ewo_number);
CREATE INDEX IF NOT EXISTS idx_ewos_opp ON enterprise.ewos(opportunity_id);
CREATE INDEX IF NOT EXISTS idx_ewos_status ON enterprise.ewos(status);
CREATE INDEX IF NOT EXISTS idx_ewos_assigned ON enterprise.ewos(assigned_to);

CREATE TRIGGER trg_ewos_touch
    BEFORE UPDATE ON enterprise.ewos
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- RBAC permissions
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'invoice.read',     'View enterprise invoices + payments'),
    ('enterprise', 'invoice.manage',   'Issue + void enterprise invoices'),
    ('enterprise', 'payment.record',   'Record a payment against an invoice'),
    ('enterprise', 'ewo.read',         'View engineering work orders'),
    ('enterprise', 'ewo.manage',       'Assign / start / complete / cancel EWOs')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'invoice.read', 'invoice.manage', 'payment.record',
      'ewo.read', 'ewo.manage'
  )
ON CONFLICT DO NOTHING;

-- sales_manager: read + EWO manage (oversees the handoff to fulfillment).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'enterprise'
  AND p.action IN ('invoice.read', 'ewo.read', 'ewo.manage')
ON CONFLICT DO NOTHING;

-- sales_rep: read-only on both (their own deals).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'enterprise'
  AND p.action IN ('invoice.read', 'ewo.read')
ON CONFLICT DO NOTHING;

-- finance: invoice + payment surface (no ewo).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance'
  AND p.module = 'enterprise'
  AND p.action IN ('invoice.read', 'invoice.manage', 'payment.record')
ON CONFLICT DO NOTHING;

COMMIT;
