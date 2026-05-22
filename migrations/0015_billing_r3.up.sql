-- M6 r3 — auto-termination follow-through, voluntary termination,
-- referral reward, and a customer activated_at anchor for anniversary
-- billing.
--
-- Adds:
--   1. crm.customers.activated_at + crm.customers.lock_in_until
--   2. crm.referrals — referrer_user/customer + referee customer + status
--   3. billing.termination_requests — voluntary + auto request rows
--   4. billing.referral_rewards — payout ledger
--   5. permission seeds + role grants

BEGIN;

-- =====================================================================
-- 1. crm.customers — anniversary + lock-in support
-- =====================================================================
ALTER TABLE crm.customers
    ADD COLUMN IF NOT EXISTS activated_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS lock_in_until DATE,
    ADD COLUMN IF NOT EXISTS referral_code TEXT UNIQUE;

CREATE INDEX IF NOT EXISTS idx_crm_customers_activated_at ON crm.customers (activated_at);

-- Backfill activated_at from BAST NOC verification for any existing active
-- customer so the anniversary cadence has an anchor on day-1.
UPDATE crm.customers c
   SET activated_at = sub.first_verified
  FROM (
       SELECT wo.customer_id, MIN(b.noc_verified_at) AS first_verified
         FROM field.bast_records b
         JOIN field.work_orders wo ON wo.id = b.wo_id
        WHERE b.noc_verified_at IS NOT NULL
        GROUP BY wo.customer_id
       ) sub
 WHERE c.id = sub.customer_id
   AND c.activated_at IS NULL;

-- Trigger: every time a BAST flips to noc_status='approved', stamp the
-- customer's activated_at if it's still null. This keeps activated_at
-- as a first-class column without needing every service to remember to
-- write it.
CREATE OR REPLACE FUNCTION crm.stamp_customer_activated() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.noc_status = 'approved' AND NEW.noc_verified_at IS NOT NULL THEN
        UPDATE crm.customers c
           SET activated_at = COALESCE(c.activated_at, NEW.noc_verified_at),
               updated_at = NOW()
          FROM field.work_orders wo
         WHERE wo.id = NEW.wo_id
           AND c.id = wo.customer_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_stamp_customer_activated ON field.bast_records;
CREATE TRIGGER trg_stamp_customer_activated
AFTER INSERT OR UPDATE OF noc_status, noc_verified_at ON field.bast_records
FOR EACH ROW EXECUTE FUNCTION crm.stamp_customer_activated();

-- =====================================================================
-- 2. crm.referrals — one row per (referrer, referee_customer)
-- =====================================================================
CREATE TABLE IF NOT EXISTS crm.referrals (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    referrer_customer_id UUID REFERENCES crm.customers(id) ON DELETE SET NULL,
    referee_customer_id  UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    referrer_code        TEXT,
    status               TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','rewarded','void')),
    rewarded_at          TIMESTAMPTZ,
    notes                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (referee_customer_id)
);
CREATE INDEX IF NOT EXISTS idx_crm_referrals_referrer ON crm.referrals (referrer_customer_id);
CREATE INDEX IF NOT EXISTS idx_crm_referrals_status   ON crm.referrals (status);

-- =====================================================================
-- 3. billing.termination_requests — both flows write here
-- =====================================================================
-- kind:
--   voluntary  — customer-initiated (or finance on behalf)
--   auto       — driven by suspension threshold
--
-- status:
--   requested        — created; balance/lock-in check pending
--   awaiting_payment — balance owed; final invoice issued
--   wo_pending       — final invoice settled; field WO to mint
--   wo_created       — termination WO exists (we stamp its id)
--   completed        — termination WO closed; customer terminated
--   cancelled        — customer settled / disputed / reactivated
CREATE TABLE IF NOT EXISTS billing.termination_requests (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id           UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    order_id              UUID REFERENCES crm.orders(id) ON DELETE SET NULL,
    kind                  TEXT NOT NULL CHECK (kind IN ('voluntary','auto')),
    status                TEXT NOT NULL DEFAULT 'requested'
        CHECK (status IN ('requested','awaiting_payment','wo_pending','wo_created','completed','cancelled')),
    reason                TEXT,
    requested_by_user_id  UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    final_invoice_id      UUID REFERENCES billing.invoices(id) ON DELETE SET NULL,
    penalty_amount        NUMERIC(14,2) NOT NULL DEFAULT 0,
    outstanding_at_request NUMERIC(14,2) NOT NULL DEFAULT 0,
    wo_id                 UUID,
    requested_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at          TIMESTAMPTZ,
    notes                 TEXT,
    UNIQUE (customer_id, kind, status)
        DEFERRABLE INITIALLY DEFERRED
);
-- We can't fully unique on (customer_id, kind) because the same customer
-- might (rarely) be re-terminated after reactivation. The deferred
-- partial unique above is broad enough; specific business rules are
-- enforced at the usecase layer.
CREATE INDEX IF NOT EXISTS idx_term_req_customer ON billing.termination_requests (customer_id);
CREATE INDEX IF NOT EXISTS idx_term_req_status   ON billing.termination_requests (status);
CREATE INDEX IF NOT EXISTS idx_term_req_kind     ON billing.termination_requests (kind);

-- =====================================================================
-- 4. billing.referral_rewards — payout ledger
-- =====================================================================
CREATE TABLE IF NOT EXISTS billing.referral_rewards (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    referral_id         UUID NOT NULL REFERENCES crm.referrals(id) ON DELETE CASCADE,
    referrer_customer_id UUID REFERENCES crm.customers(id) ON DELETE SET NULL,
    referee_customer_id  UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    order_id             UUID REFERENCES crm.orders(id) ON DELETE SET NULL,
    invoice_id           UUID REFERENCES billing.invoices(id) ON DELETE SET NULL,
    amount               NUMERIC(14,2) NOT NULL,
    status               TEXT NOT NULL DEFAULT 'accrued'
        CHECK (status IN ('accrued','paid','void')),
    paid_at              TIMESTAMPTZ,
    notes                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (referral_id)
);
CREATE INDEX IF NOT EXISTS idx_referral_rewards_referrer ON billing.referral_rewards (referrer_customer_id);
CREATE INDEX IF NOT EXISTS idx_referral_rewards_status   ON billing.referral_rewards (status);

-- =====================================================================
-- 5. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('billing', 'termination.request', 'Initiate a voluntary termination request'),
    ('billing', 'termination.read',    'View termination requests'),
    ('billing', 'termination.manage',  'Cancel / progress termination requests'),
    ('billing', 'referral.read',       'View referral rewards'),
    ('crm',     'referral.manage',     'Create / void referral records')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module IN ('billing','crm')
  AND p.action IN ('termination.request','termination.read','termination.manage','referral.read','referral.manage')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('finance_admin','finance_manager')
  AND p.module = 'billing'
  AND p.action IN ('termination.request','termination.read','termination.manage','referral.read')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager','sales_rep')
  AND p.module = 'crm'
  AND p.action = 'referral.manage'
ON CONFLICT DO NOTHING;

COMMIT;
