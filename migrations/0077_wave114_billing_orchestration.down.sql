-- Wave 114 — rollback for billing orchestration cron tables.

BEGIN;

DROP TABLE IF EXISTS billing.commission_triggers;
DROP TABLE IF EXISTS billing.suspension_actions;
DROP TABLE IF EXISTS billing.late_fee_applications;
DROP TABLE IF EXISTS billing.reminder_log;

COMMIT;
