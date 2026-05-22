-- 0025_pricebook_versioning_fix.down.sql
BEGIN;

ALTER TABLE enterprise.pricebooks
    DROP CONSTRAINT IF EXISTS uq_pricebooks_code_version;

ALTER TABLE enterprise.pricebooks
    ADD CONSTRAINT pricebooks_code_key UNIQUE (code);

COMMIT;
