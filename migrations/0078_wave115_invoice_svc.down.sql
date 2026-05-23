-- Wave 115 — rollback for Invoice Svc extraction + Add-On Billing.

BEGIN;

DROP TABLE IF EXISTS billing.add_on_purchases;
DROP SCHEMA IF EXISTS invoicesvc CASCADE;

COMMIT;
