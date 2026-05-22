BEGIN;
DROP TABLE IF EXISTS crm.customer_sessions;
ALTER TABLE crm.customer_portal_otp
    DROP CONSTRAINT IF EXISTS customer_portal_otp_purpose_check;
ALTER TABLE crm.customer_portal_otp
    ADD CONSTRAINT customer_portal_otp_purpose_check
    CHECK (purpose IN ('termination'));
ALTER TABLE crm.customer_portal_otp
    ALTER COLUMN purpose SET DEFAULT 'termination';
COMMIT;
