DELETE FROM identity.role_permissions
WHERE permission_id IN (SELECT id FROM identity.permissions WHERE module = 'billing');
DELETE FROM identity.permissions WHERE module = 'billing';

DROP TABLE IF EXISTS billing.payments;
DROP TABLE IF EXISTS billing.invoice_items;
DROP TABLE IF EXISTS billing.invoices;
DROP SCHEMA IF EXISTS billing CASCADE;
