-- Reverse of 0049_wave76_qa_compliance.up.sql.

BEGIN;

-- TC-RAD-019 — drop NOC credential permission
DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'network' AND action = 'radius.regenerate'
);
DELETE FROM identity.permissions
WHERE module = 'network' AND action = 'radius.regenerate';

-- TC-CRM-014 — drop config row
DELETE FROM identity.platform_config WHERE config_key = 'lead_overdue_days';

-- TC-PRD-021 — drop schema customer_type column
DROP INDEX IF EXISTS platform.idx_schema_def_kind_customer_status;
ALTER TABLE platform.schema_definitions DROP COLUMN IF EXISTS customer_type;

-- TC-CRM-007/008 — drop referrer column
DROP INDEX IF EXISTS crm.idx_crm_leads_referrer;
ALTER TABLE crm.leads DROP COLUMN IF EXISTS referrer_customer_id;

-- TC-CRM-002 — drop lead_type column
DROP INDEX IF EXISTS crm.idx_crm_leads_lead_type;
ALTER TABLE crm.leads DROP COLUMN IF EXISTS lead_type;

-- TC-CRM-006 — revert source enum to legacy 5
ALTER TABLE crm.leads DROP CONSTRAINT IF EXISTS leads_source_check;
ALTER TABLE crm.leads ADD CONSTRAINT leads_source_check
    CHECK (source IN ('manual','self_order','sales_app','referral','cs_referral'));

COMMIT;
