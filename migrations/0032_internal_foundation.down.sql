-- 0032_internal_foundation.down.sql
BEGIN;

ALTER TABLE enterprise.boq_versions DROP COLUMN IF EXISTS source_rfq_id;

DROP TABLE IF EXISTS enterprise.notification_preferences;
DROP TABLE IF EXISTS enterprise.ewo_checklist_templates;
DROP TABLE IF EXISTS platform.customer_schema_overrides;
DROP TABLE IF EXISTS platform.schema_definitions;
DROP TYPE IF EXISTS platform.schema_kind;
DROP TABLE IF EXISTS enterprise.internal_transactions;

COMMIT;
