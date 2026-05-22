DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'billing'
      AND action IN ('cycles.read','cycles.run','policy.read','policy.manage','commission.read')
);
DELETE FROM identity.permissions
WHERE module = 'billing'
  AND action IN ('cycles.read','cycles.run','policy.read','policy.manage','commission.read');

ALTER TABLE crm.customers
    DROP COLUMN IF EXISTS suspended_at,
    DROP COLUMN IF EXISTS suspend_reason,
    DROP COLUMN IF EXISTS terminated_at;

DROP TABLE IF EXISTS billing.commission_records;
DROP TABLE IF EXISTS billing.billing_cycles;
DROP TABLE IF EXISTS billing.policies;
