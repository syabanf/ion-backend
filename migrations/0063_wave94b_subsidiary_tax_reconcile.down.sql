-- Down — restore is_pkp + ppn_rate on enterprise.subsidiaries and
-- backfill from the latest tax_profile per subsidiary so live data
-- isn't lost on a rollback.

BEGIN;

ALTER TABLE enterprise.subsidiaries
    ADD COLUMN is_pkp   BOOLEAN       NOT NULL DEFAULT FALSE,
    ADD COLUMN ppn_rate NUMERIC(5, 4) NOT NULL DEFAULT 0.11;

-- Backfill from the most recent tax_profile per subsidiary, when one
-- exists. Subsidiaries without a tax_profile keep the column defaults.
UPDATE enterprise.subsidiaries s
   SET is_pkp   = tp.is_pkp,
       ppn_rate = tp.ppn_rate
  FROM (
        SELECT DISTINCT ON (subsidiary_id)
               subsidiary_id, is_pkp, ppn_rate
          FROM tax.company_tax_profiles
         ORDER BY subsidiary_id, effective_from DESC
       ) tp
 WHERE s.id = tp.subsidiary_id;

COMMIT;
