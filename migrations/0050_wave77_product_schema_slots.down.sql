-- Reverse of 0050_wave77_product_schema_slots.up.sql.

BEGIN;

DROP INDEX IF EXISTS crm.idx_crm_products_suspension_schema;
DROP INDEX IF EXISTS crm.idx_crm_products_commission_schema;
DROP INDEX IF EXISTS crm.idx_crm_products_billing_schema;
DROP INDEX IF EXISTS crm.idx_crm_products_service_schema;
DROP INDEX IF EXISTS crm.idx_crm_products_onboarding_schema;

ALTER TABLE crm.products
    DROP COLUMN IF EXISTS suspension_schema_id,
    DROP COLUMN IF EXISTS commission_schema_id,
    DROP COLUMN IF EXISTS service_schema_id,
    DROP COLUMN IF EXISTS billing_schema_id,
    DROP COLUMN IF EXISTS onboarding_schema_id;

COMMIT;
