-- Wave 94b — Subsidiary ↔ tax_profile source-of-truth reconciliation.
--
-- Background: Waves 92 + 93 landed in parallel agents. Wave 92 put
-- `is_pkp` + `ppn_rate` on `enterprise.subsidiaries`. Wave 93 created
-- `tax.company_tax_profiles` with `is_pkp` + `ppn_rate` + an
-- `effective_from` window. Two sources of truth for the same fact
-- is a sync hazard — invoice generation needs the effective-window
-- answer ("is this co PKP on the invoice date") not "is this co PKP
-- right now," so the tax_profile row is canonical.
--
-- This migration:
--   1. Backfills tax_profile rows for the two demo subsidiaries seeded
--      by 0060 so dropping the columns doesn't lose data.
--   2. Drops `is_pkp` + `ppn_rate` from `enterprise.subsidiaries`.
--
-- Every consumer that needs "is this subsidiary PKP" now reads via
-- `tax.usecase.Service.GetActiveProfile(ctx, subsidiaryID, at)`.

BEGIN;

-- =====================================================================
-- 1. Backfill: ensure every existing subsidiary has at least one
--    tax_profile row carrying its previous is_pkp / ppn_rate. If a
--    tax_profile already exists for the (subsidiary_id, effective_from)
--    pair, leave it alone.
-- =====================================================================
INSERT INTO tax.company_tax_profiles
    (subsidiary_id, name, npwp, is_pkp, ppn_rate, pph23_rate, pph_final_rate, effective_from)
SELECT
    s.id,
    s.name,
    s.npwp,
    s.is_pkp,
    s.ppn_rate,
    0.02,            -- pph23 default (matches 0061 seed)
    0.00,            -- pph_final default
    DATE '2024-01-01'
FROM enterprise.subsidiaries s
ON CONFLICT (subsidiary_id, effective_from) DO NOTHING;

-- =====================================================================
-- 2. Drop the redundant columns. tax.company_tax_profiles is now the
--    only source of truth for `is_pkp` + `ppn_rate`.
-- =====================================================================
ALTER TABLE enterprise.subsidiaries DROP COLUMN is_pkp;
ALTER TABLE enterprise.subsidiaries DROP COLUMN ppn_rate;

COMMIT;
