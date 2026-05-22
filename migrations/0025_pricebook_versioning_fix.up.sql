-- 0025_pricebook_versioning_fix.up.sql
--
-- 0024 accidentally put `UNIQUE` on `enterprise.pricebooks.code` alone.
-- That blocks the versioning model — a draft v2 of the same code can't
-- exist alongside the published v1.
--
-- The correct unique constraints for a versioned catalog:
--   (1) Only ONE published pricebook per code at a time — already in
--       place via partial index `uq_pricebooks_published_per_code`.
--   (2) Version numbers within a code don't repeat —
--       (code, version_no) unique.
--
-- The application-layer overlap check (domain.Overlaps) is what
-- prevents two distinct code values with overlapping windows from
-- being problematic for callers; that lives in the usecase.

BEGIN;

ALTER TABLE enterprise.pricebooks
    DROP CONSTRAINT IF EXISTS pricebooks_code_key;

-- Multiple versions of the same code are now allowed; just no two with
-- the same version_no.
ALTER TABLE enterprise.pricebooks
    ADD CONSTRAINT uq_pricebooks_code_version
    UNIQUE (code, version_no);

COMMIT;
