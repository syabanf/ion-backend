-- 0030_enterprise_pre_launch.down.sql
BEGIN;

DROP TABLE IF EXISTS enterprise.rfqs;
DROP TABLE IF EXISTS enterprise.enterprise_services;
DROP TABLE IF EXISTS enterprise.project_sites;
DROP TABLE IF EXISTS enterprise.projects;
DROP TABLE IF EXISTS enterprise.notifications;
DROP TABLE IF EXISTS enterprise.ewo_checklist_items;
DROP TABLE IF EXISTS enterprise.payment_proofs;
DROP TABLE IF EXISTS enterprise.po_documents;

ALTER TABLE enterprise.invoices
    DROP COLUMN IF EXISTS invoice_plan_id,
    DROP COLUMN IF EXISTS invoice_plan_item_id,
    DROP COLUMN IF EXISTS subtotal_amount,
    DROP COLUMN IF EXISTS tax_pct,
    DROP COLUMN IF EXISTS tax_amount;

DROP TABLE IF EXISTS enterprise.invoice_plan_items;
DROP TABLE IF EXISTS enterprise.invoice_plans;

ALTER TABLE enterprise.boq_versions
    DROP COLUMN IF EXISTS subtotal_amount,
    DROP COLUMN IF EXISTS tax_pct,
    DROP COLUMN IF EXISTS tax_amount;

DROP TABLE IF EXISTS enterprise.boq_line_reminders;
ALTER TABLE enterprise.boq_lines
    DROP COLUMN IF EXISTS vendor_due_at;

ALTER TABLE enterprise.ewos
    DROP COLUMN IF EXISTS field_work_order_id,
    DROP COLUMN IF EXISTS progress_pct;

ALTER TABLE enterprise.approval_instances
    DROP COLUMN IF EXISTS reset_reason;

COMMIT;
