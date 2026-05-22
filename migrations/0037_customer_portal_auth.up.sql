-- 0037 — customer-portal authentication.
--
-- Extends the existing crm.customer_portal_otp table (purpose was
-- previously limited to 'termination') to also carry 'login' OTPs,
-- and adds a customer_sessions table so the mobile customer_app can
-- mint a JWT after OTP verification and keep the customer signed in
-- without prompting for OTP on every cold start.
--
-- The JWT itself isn't persisted — only a refresh handle is, so we
-- can revoke a device session without rotating the global signing key.

BEGIN;

-- Widen the OTP purpose enum to support login. The existing CHECK
-- constraint must be dropped + re-added (no ALTER CHECK in pg).
ALTER TABLE crm.customer_portal_otp
    DROP CONSTRAINT IF EXISTS customer_portal_otp_purpose_check;
ALTER TABLE crm.customer_portal_otp
    ALTER COLUMN purpose DROP DEFAULT;
ALTER TABLE crm.customer_portal_otp
    ADD CONSTRAINT customer_portal_otp_purpose_check
    CHECK (purpose IN ('termination','login','plan_change','address_change'));
ALTER TABLE crm.customer_portal_otp
    ALTER COLUMN purpose SET DEFAULT 'login';

-- Customer-side sessions (refresh tokens). Each row is one device.
-- The mobile app stores the refresh_token plaintext in secure storage;
-- we keep only the bcrypt hash here so a DB leak can't replay sessions.
CREATE TABLE IF NOT EXISTS crm.customer_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    refresh_hash    TEXT NOT NULL,
    device_label    TEXT,
    user_agent      TEXT,
    ip_address      TEXT,
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    last_seen_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_customer_sessions_customer
    ON crm.customer_sessions (customer_id)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_sessions_expires
    ON crm.customer_sessions (expires_at)
    WHERE revoked_at IS NULL;

COMMIT;
