-- Reverse of 0051_wave78_customer_schema_lock.up.sql.

BEGIN;

DROP INDEX IF EXISTS crm.idx_customer_locked_suspension;
DROP INDEX IF EXISTS crm.idx_customer_locked_commission;
DROP INDEX IF EXISTS crm.idx_customer_locked_service;
DROP INDEX IF EXISTS crm.idx_customer_locked_billing;
DROP INDEX IF EXISTS crm.idx_customer_locked_onboarding;

ALTER TABLE crm.customers
    DROP COLUMN IF EXISTS locked_suspension_schema_version_id,
    DROP COLUMN IF EXISTS locked_commission_schema_version_id,
    DROP COLUMN IF EXISTS locked_service_schema_version_id,
    DROP COLUMN IF EXISTS locked_billing_schema_version_id,
    DROP COLUMN IF EXISTS locked_onboarding_schema_version_id;

COMMIT;
