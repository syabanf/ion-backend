-- 0031_invoices_allow_termin.down.sql
BEGIN;
DROP INDEX IF EXISTS enterprise.uq_invoices_quotation_direct;
-- Restore original full uniqueness — note this requires no termin
-- invoices exist on rollback or the index creation will fail.
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoices_quotation
    ON enterprise.invoices(quotation_id);
COMMIT;
