BEGIN;

ALTER TABLE crm.customers DROP COLUMN IF EXISTS nik_encrypted;
ALTER TABLE crm.leads     DROP COLUMN IF EXISTS nik_encrypted;

COMMIT;
