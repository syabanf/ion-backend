-- Drop M4 CRM round 1.
DELETE FROM identity.role_permissions
WHERE permission_id IN (SELECT id FROM identity.permissions WHERE key LIKE 'crm.%');
DELETE FROM identity.permissions WHERE key LIKE 'crm.%';

ALTER TABLE crm.leads
  DROP CONSTRAINT IF EXISTS leads_converted_customer_fk,
  DROP CONSTRAINT IF EXISTS leads_converted_order_fk;

DROP TABLE IF EXISTS crm.orders;
DROP TABLE IF EXISTS crm.customers;
DROP TABLE IF EXISTS crm.order_documents;
DROP TABLE IF EXISTS crm.leads;
DROP TABLE IF EXISTS crm.products;

DROP SCHEMA IF EXISTS crm CASCADE;
