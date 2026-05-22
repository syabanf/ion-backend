-- 0040 — Suggested-priority follow-ups (post-Wave-2)
--
-- Adds the schema surface required to close the remaining 🟥/🟧 gaps
-- in docs/gap-analysis.md:
--
--   • Self-order lead funnel (no schema — uses crm.leads + crm.lead_sources)
--   • Payment integration (billing.invoices + billing.payment_intents)
--   • Faktur Pajak fields on invoices
--   • Push notifications (platform.device_tokens)
--   • Live GPS streaming (field.tech_locations)
--   • HRIS sync (identity.hris_sync_state)
--
-- No data is dropped; every column is nullable so existing rows keep
-- working.

BEGIN;

-- ============================================================
-- 1. Billing — payment surface for the customer portal
-- ============================================================

ALTER TABLE billing.invoices
    ADD COLUMN IF NOT EXISTS faktur_pajak_number TEXT,
    ADD COLUMN IF NOT EXISTS faktur_pajak_issued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS checkout_url TEXT,
    ADD COLUMN IF NOT EXISTS va_number TEXT,
    ADD COLUMN IF NOT EXISTS va_bank_code TEXT,
    ADD COLUMN IF NOT EXISTS payment_due_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS billing.payment_intents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id      UUID NOT NULL REFERENCES billing.invoices(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL,
    method          TEXT NOT NULL CHECK (method IN ('xendit_va','xendit_ewallet','xendit_qris','manual_transfer','cash')),
    amount          NUMERIC(15,2) NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','succeeded','failed','expired','cancelled')),
    -- Gateway shape — kept generic so we can plug providers later
    gateway_ref     TEXT,         -- Xendit invoice id / VA id
    gateway_payload JSONB,        -- full request/response for forensics
    checkout_url    TEXT,         -- short-lived link the customer opens
    va_number       TEXT,         -- bank-VA shape
    va_bank_code    TEXT,         -- BCA / BNI / Mandiri / etc.
    expires_at      TIMESTAMPTZ,
    confirmed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pay_intents_invoice
    ON billing.payment_intents (invoice_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_pay_intents_customer
    ON billing.payment_intents (customer_id, created_at DESC);

-- ============================================================
-- 2. Push notifications — device token registry
-- ============================================================

CREATE TABLE IF NOT EXISTS platform.device_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID,    -- staff app
    customer_id UUID,    -- customer app
    token       TEXT NOT NULL,
    platform    TEXT NOT NULL CHECK (platform IN ('ios','android','web')),
    app         TEXT NOT NULL CHECK (app IN ('tech','sales','customer','staff_web')),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((user_id IS NULL) <> (customer_id IS NULL)),
    UNIQUE (token)
);

CREATE INDEX IF NOT EXISTS idx_device_tokens_user
    ON platform.device_tokens (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_device_tokens_customer
    ON platform.device_tokens (customer_id) WHERE customer_id IS NOT NULL;

-- ============================================================
-- 3. Live GPS streaming — tech_locations ping table
-- ============================================================

CREATE TABLE IF NOT EXISTS field.tech_locations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL,
    wo_id       UUID,                 -- nullable: pre-journey heartbeats
    lat         DOUBLE PRECISION NOT NULL,
    lng         DOUBLE PRECISION NOT NULL,
    accuracy_m  DOUBLE PRECISION,
    speed_mps   DOUBLE PRECISION,
    heading_deg DOUBLE PRECISION,
    captured_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index pattern: latest-by-user (Team Leader live map),
-- and time-window-by-WO (job replay).
CREATE INDEX IF NOT EXISTS idx_tech_loc_user_time
    ON field.tech_locations (user_id, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_tech_loc_wo_time
    ON field.tech_locations (wo_id, captured_at DESC) WHERE wo_id IS NOT NULL;

-- ============================================================
-- 4. HRIS sync — cache + status state
-- ============================================================

CREATE TABLE IF NOT EXISTS identity.hris_sync_state (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider        TEXT NOT NULL,         -- 'mekari', 'gajiku', 'manual_csv', 'stub'
    last_run_at     TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_error      TEXT,
    rows_synced     INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider)
);

ALTER TABLE identity.user_availability
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual','hris','self_service','schedule_template'));

-- ============================================================
-- 5. Permissions for new endpoints
--
-- identity.permissions uses (module, action) — action is the full
-- dotted code as used in code, e.g. "field.tech_location.write".
-- ============================================================

-- identity.permissions stores `action` WITHOUT the module prefix;
-- PermissionsForUser concatenates `module || '.' || action` to form
-- the canonical "module.action" key checked by RequirePermission.
INSERT INTO identity.permissions (module, action, description) VALUES
    ('billing','invoice.pay','Initiate a payment intent for an invoice'),
    ('field','tech_location.write','POST own GPS ping during active WO'),
    ('field','tech_location.read','Read other techs'' live locations (Team Leader)'),
    ('platform','device_token.register','Register a device push token')
ON CONFLICT (module, action) DO NOTHING;

-- Grant the new perms to obvious roles. (Customer portal users get
-- billing.invoice.pay through the existing portal-access claim — they
-- don't go through identity.roles.)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'technician'
  AND (p.module || '.' || p.action) IN ('field.tech_location.write','platform.device_token.register')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('team_leader','operations_admin','super_admin')
  AND (p.module || '.' || p.action) = 'field.tech_location.read'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('sales_rep','sales_manager','technician','operations_admin','super_admin')
  AND (p.module || '.' || p.action) = 'platform.device_token.register'
ON CONFLICT DO NOTHING;

COMMIT;
