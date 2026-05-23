-- Wave 107 — Finance Client AR residual columns.
--
-- Adds the three columns the Wave 107 finance polish needs:
--   - reminder_sent_at         — last cron-triggered reminder timestamp
--                                (one reminder per due cycle).
--   - pph23_withheld_amount    — withholding the customer kept; the
--                                settlement view computes net_received
--                                as total_amount - pph23_withheld.
--   - is_pph23_applicable      — manual flag set by finance per invoice
--                                (corporate customer withholds PPh23).
--
-- Plus an index on (status, due_at) limited to open / partial invoices
-- so the reminder cron's scan is index-only.

BEGIN;

ALTER TABLE enterprise.invoices
    ADD COLUMN IF NOT EXISTS reminder_sent_at         TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS pph23_withheld_amount    NUMERIC(18,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS is_pph23_applicable      BOOLEAN       NOT NULL DEFAULT FALSE;

-- Cron-driven scan: invoices with status in ('open','partial','issued')
-- and due_at within 3 days. Status 'issued' is treated as "open" by the
-- invoice lifecycle in this codebase (see domain/invoice.go); we index
-- all three pre-paid states so legacy + new rows are both covered.
CREATE INDEX IF NOT EXISTS idx_enterprise_invoices_reminder_scan
    ON enterprise.invoices (status, due_at)
    WHERE status IN ('issued', 'partial');

COMMIT;
