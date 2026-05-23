-- Wave 107 — rollback for finance polish columns.

BEGIN;

DROP INDEX IF EXISTS enterprise.idx_enterprise_invoices_reminder_scan;

ALTER TABLE enterprise.invoices
    DROP COLUMN IF EXISTS reminder_sent_at,
    DROP COLUMN IF EXISTS pph23_withheld_amount,
    DROP COLUMN IF EXISTS is_pph23_applicable;

COMMIT;
