DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'crm' AND action IN ('schema.read','schema.manage','dashboard.read')
);
DELETE FROM identity.permissions
WHERE module = 'crm' AND action IN ('schema.read','schema.manage','dashboard.read');

ALTER TABLE crm.leads
    DROP COLUMN IF EXISTS onboarding_schema_id,
    DROP COLUMN IF EXISTS sales_type_at_create;

DROP TABLE IF EXISTS crm.onboarding_schemas;
