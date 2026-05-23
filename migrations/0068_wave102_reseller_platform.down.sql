-- Wave 102 down — drop the three Wave 102 tables and clean up the new
-- permissions / role grants. We leave the `reseller` schema in place
-- (Wave 94 still owns reseller_accounts, wholesale_*, platform_sessions).
--
-- Order matters: subscriber_invoices has a RESTRICT FK to subscribers,
-- so subscriber_imports → subscriber_invoices → subscribers.

BEGIN;

DROP TABLE IF EXISTS reseller.subscriber_imports;
DROP TABLE IF EXISTS reseller.subscriber_invoices;
DROP TABLE IF EXISTS reseller.subscribers;

-- Permission cleanup. We remove the role_permission rows first so the
-- permission rows aren't orphaned references; the role itself stays
-- because `reseller_admin` may be reused by future waves.
DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'reseller'
      AND action IN ('subscriber.read','subscriber.write','invoice.read','invoice.write','dashboard.read')
);

DELETE FROM identity.permissions
WHERE module = 'reseller'
  AND action IN ('subscriber.read','subscriber.write','invoice.read','invoice.write','dashboard.read');

COMMIT;
