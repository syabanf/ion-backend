-- Wave 101 — rollback.
BEGIN;

DROP INDEX IF EXISTS enterprise.idx_internal_transactions_superseded_at;
DROP INDEX IF EXISTS enterprise.idx_internal_transactions_source_event;
ALTER TABLE enterprise.internal_transactions
    DROP COLUMN IF EXISTS source_event,
    DROP COLUMN IF EXISTS superseded_at;

ALTER TABLE tax.faktur_pajak_records
    DROP COLUMN IF EXISTS dpp_decoded,
    DROP COLUMN IF EXISTS tax_snapshot_hash;

DROP INDEX IF EXISTS enterprise.idx_invoices_faktur_pajak;
ALTER TABLE enterprise.invoices
    DROP COLUMN IF EXISTS faktur_pajak_id,
    DROP COLUMN IF EXISTS tax_snapshot_hash;

ALTER TABLE enterprise.quotations
    DROP COLUMN IF EXISTS tax_snapshot_hash;

DROP INDEX IF EXISTS enterprise.idx_boq_versions_tax_profile;
ALTER TABLE enterprise.boq_versions
    DROP COLUMN IF EXISTS tax_profile_id,
    DROP COLUMN IF EXISTS tax_snapshot_hash;

COMMIT;
