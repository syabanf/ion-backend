-- 0043_webhook_deliveries.down.sql
--
-- Reverts 0043_webhook_deliveries.up.sql. Drops the webhook delivery
-- store. The index drops with the parent table.
--
-- WARNING: this loses the forensic audit trail of every inbound
-- webhook. Production rollback should export the table first if
-- you might ever need to replay or investigate a delivery.

BEGIN;

DROP TABLE IF EXISTS platform.webhook_deliveries;

COMMIT;
