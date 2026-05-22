-- M6 r3 — customer-portal OTP table.
--
-- Powers the self-service voluntary-termination flow: the customer
-- enters customer_number + phone, we mint a 6-digit OTP, store its
-- bcrypt hash, and dispatch it out-of-band (round-3: log only; round-4:
-- WhatsApp/SMS). The customer then submits the plaintext OTP + reason
-- and we verify against the hash before invoking the standard staff-
-- side RequestVoluntaryTermination flow.
--
-- The table is purpose-scoped — `purpose` is currently always
-- 'termination', but we leave the column so future flows (address
-- change, plan downgrade, …) can land here without another migration.
--
-- TTL: rows are valid for 10 minutes from `created_at`. A janitor in
-- the usecase deletes expired rows on every verify call; we don't bother
-- with a cron table-cleanup for round 1.

BEGIN;

CREATE TABLE IF NOT EXISTS crm.customer_portal_otp (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    purpose         TEXT NOT NULL DEFAULT 'termination'
        CHECK (purpose IN ('termination')),
    otp_hash        TEXT NOT NULL,
    attempts        INT  NOT NULL DEFAULT 0,
    verified_at     TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customer_otp_customer  ON crm.customer_portal_otp (customer_id);
CREATE INDEX IF NOT EXISTS idx_customer_otp_expires   ON crm.customer_portal_otp (expires_at);

COMMIT;
