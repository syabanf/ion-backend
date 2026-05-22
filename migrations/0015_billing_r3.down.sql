BEGIN;

DROP TRIGGER IF EXISTS trg_stamp_customer_activated ON field.bast_records;
DROP FUNCTION IF EXISTS crm.stamp_customer_activated();

DELETE FROM identity.role_permissions
 USING identity.permissions p
 WHERE p.id = identity.role_permissions.permission_id
   AND ((p.module = 'billing' AND p.action IN ('termination.request','termination.read','termination.manage','referral.read'))
        OR (p.module = 'crm' AND p.action = 'referral.manage'));

DELETE FROM identity.permissions
 WHERE (module = 'billing' AND action IN ('termination.request','termination.read','termination.manage','referral.read'))
    OR (module = 'crm'     AND action = 'referral.manage');

DROP TABLE IF EXISTS billing.referral_rewards;
DROP TABLE IF EXISTS billing.termination_requests;
DROP TABLE IF EXISTS crm.referrals;

ALTER TABLE crm.customers
    DROP COLUMN IF EXISTS activated_at,
    DROP COLUMN IF EXISTS lock_in_until,
    DROP COLUMN IF EXISTS referral_code;

COMMIT;
