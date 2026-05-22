-- 0031_invoices_allow_termin.up.sql
--
-- Relax the "one invoice per quotation" uniqueness constraint so that
-- termin invoice plans (Phase 5 / E5) can produce multiple invoices
-- against the same quotation. The IDEMPOTENCY contract moves from a
-- DB index to use-case logic:
--
--   - IssueInvoice (direct path from quotation accept) — only one
--     direct invoice (invoice_plan_id IS NULL) per quotation. We
--     enforce via a partial unique index.
--   - Termin items — each item produces its own invoice with
--     invoice_plan_item_id set. No DB-side uniqueness; the plan_item
--     repo prevents double-issue.
BEGIN;

DROP INDEX IF EXISTS enterprise.uq_invoices_quotation;

-- Partial unique: one direct invoice per quotation. Termin invoices
-- (which carry invoice_plan_item_id) are exempt.
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoices_quotation_direct
    ON enterprise.invoices(quotation_id)
    WHERE invoice_plan_item_id IS NULL;

COMMIT;
