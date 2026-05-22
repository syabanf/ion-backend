-- 0029_enterprise_finance.down.sql
BEGIN;

DROP TABLE IF EXISTS enterprise.invoice_payments;
DROP TABLE IF EXISTS enterprise.invoices;
DROP TABLE IF EXISTS enterprise.ewos;

-- Permissions linger; if you really want them gone:
-- DELETE FROM identity.role_permissions
--   WHERE permission_id IN (
--     SELECT id FROM identity.permissions WHERE module = 'enterprise'
--       AND action IN ('invoice.read','invoice.manage','payment.record','ewo.read','ewo.manage')
--   );
-- DELETE FROM identity.permissions WHERE module = 'enterprise'
--   AND action IN ('invoice.read','invoice.manage','payment.record','ewo.read','ewo.manage');

COMMIT;
