-- Wave 116 — down: drop validator tables, demo seed rows, permission.

BEGIN;

-- Permission revoke (role_permissions cascades via FK).
DELETE FROM identity.permissions
WHERE module = 'platform' AND action = 'schema.validate';

-- Demo seed rows.
DELETE FROM platform.schema_definitions
WHERE code IN (
    'WAVE116_ONBOARDING_RESIDENTIAL',
    'WAVE116_BILLING_MONTHLY',
    'WAVE116_SERVICE_RESIDENTIAL_50M',
    'WAVE116_COMMISSION_SALES_5PARTY',
    'WAVE116_SUSPENSION_STD'
);

DROP TABLE IF EXISTS platform.schema_validation_results;
DROP TABLE IF EXISTS platform.schema_kinds;

COMMIT;
