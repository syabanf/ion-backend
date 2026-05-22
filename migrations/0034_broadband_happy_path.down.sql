DROP INDEX IF EXISTS crm.idx_crm_orders_otc_type;
ALTER TABLE crm.orders DROP COLUMN IF EXISTS otc_type;
