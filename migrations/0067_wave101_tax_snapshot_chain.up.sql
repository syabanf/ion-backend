-- Wave 101 — Tax snapshot chain (BOQ → Quotation → Invoice → Faktur)
-- + DJP real-client scaffolding columns + Wave 95b internal_transaction
-- reconciliation columns.
--
-- Scope:
--   1. Stamp the active tax_profile + a SHA-256 snapshot hash onto the
--      BOQ at approval time. The hash carries forward to the quotation,
--      invoice, and faktur so the audit trail can later prove "this
--      exact tax stance produced this exact invoice."
--   2. Add a faktur_pajak link column on enterprise.invoices so the
--      Non-PKP short-circuit + Faktur waiver path can record the
--      decision without faking a tax.faktur_pajak_records row.
--   3. Add tax_snapshot_hash + dpp_decoded columns on tax.faktur_pajak_records
--      so the real DJP client (Wave 101 Part 2) has cleaner payload
--      decoding + can verify chain integrity.
--   4. Add superseded_at + source_event columns on
--      enterprise.internal_transactions so the Wave 95b reconciliation
--      cron can flag the legacy BOQ-approval-recognition rows once the
--      canonical IC-PO-accept row lands.
--
-- No cross-schema FKs (same pattern as Wave 93/95) — the tax_profile_id
-- + faktur_pajak_id columns are plain UUIDs that the application
-- resolves at display time.

BEGIN;

-- =====================================================================
-- Part 1 — BOQ tax snapshot
-- =====================================================================
ALTER TABLE enterprise.boq_versions
    ADD COLUMN IF NOT EXISTS tax_snapshot_hash TEXT,
    ADD COLUMN IF NOT EXISTS tax_profile_id    UUID;

CREATE INDEX IF NOT EXISTS idx_boq_versions_tax_profile
    ON enterprise.boq_versions (tax_profile_id)
    WHERE tax_profile_id IS NOT NULL;

-- =====================================================================
-- Part 1 — Quotation tax snapshot (inherited from BOQ at issuance)
-- =====================================================================
ALTER TABLE enterprise.quotations
    ADD COLUMN IF NOT EXISTS tax_snapshot_hash TEXT;

-- =====================================================================
-- Part 1 — Invoice tax snapshot + faktur backlink
-- =====================================================================
ALTER TABLE enterprise.invoices
    ADD COLUMN IF NOT EXISTS tax_snapshot_hash TEXT,
    ADD COLUMN IF NOT EXISTS faktur_pajak_id   UUID;

CREATE INDEX IF NOT EXISTS idx_invoices_faktur_pajak
    ON enterprise.invoices (faktur_pajak_id)
    WHERE faktur_pajak_id IS NOT NULL;

-- =====================================================================
-- Part 2 — Faktur Pajak snapshot + decoded DPP
-- =====================================================================
ALTER TABLE tax.faktur_pajak_records
    ADD COLUMN IF NOT EXISTS tax_snapshot_hash TEXT,
    ADD COLUMN IF NOT EXISTS dpp_decoded       NUMERIC(18, 2);

-- =====================================================================
-- Part 3 — internal_transactions reconciliation columns
-- =====================================================================
--
-- superseded_at — set by the Wave 95b reconciliation cron when the
-- canonical IC-PO-accept row supersedes the legacy BOQ-approval row.
--
-- source_event  — distinguishes the two write paths so the reconciler
-- can pick a winner. Pre-Wave-95 rows are backfilled to 'boq_approval'
-- since that was the only path that existed.
ALTER TABLE enterprise.internal_transactions
    ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS source_event  TEXT;

UPDATE enterprise.internal_transactions
SET source_event = 'boq_approval'
WHERE source_event IS NULL;

CREATE INDEX IF NOT EXISTS idx_internal_transactions_source_event
    ON enterprise.internal_transactions (source_event)
    WHERE source_event IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_internal_transactions_superseded_at
    ON enterprise.internal_transactions (superseded_at)
    WHERE superseded_at IS NOT NULL;

COMMIT;
