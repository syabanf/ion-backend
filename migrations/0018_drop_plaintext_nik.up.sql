-- M7 hardening — drop the plaintext NIK column.
--
-- Sequencing:
--   1. migration 0017 added `nik_encrypted bytea` alongside `nik text`.
--   2. application code (with KTP_ENC_KEY set) started writing both
--      columns on new rows.
--   3. `cmd/ktp-backfill` migrated any remaining plaintext rows into
--      `nik_encrypted` and NULLed the plain column.
--
-- This migration enforces the invariant that no plaintext NIK can sit
-- on disk: a pre-flight check fails the migration if any row still has
-- a non-empty plaintext value AND a NULL ciphertext (a backfill that
-- silently skipped some rows would otherwise yield a permanent data
-- loss when we drop the column).
--
-- The corollary: this migration MUST run AFTER `ktp-backfill` has
-- completed in every environment. Deploy order:
--
--   1. Deploy binaries with KTP_ENC_KEY wired (round-3).
--   2. Run cmd/ktp-backfill once per environment.
--   3. Apply migration 0018.

BEGIN;

DO $$
DECLARE
    leak_count INT;
BEGIN
    SELECT COUNT(*) INTO leak_count
      FROM crm.leads
     WHERE nik IS NOT NULL
       AND nik <> ''
       AND nik_encrypted IS NULL;
    IF leak_count > 0 THEN
        RAISE EXCEPTION 'crm.leads has % rows with plaintext NIK but no ciphertext — run cmd/ktp-backfill before migrating', leak_count;
    END IF;

    SELECT COUNT(*) INTO leak_count
      FROM crm.customers
     WHERE nik IS NOT NULL
       AND nik <> ''
       AND nik_encrypted IS NULL;
    IF leak_count > 0 THEN
        RAISE EXCEPTION 'crm.customers has % rows with plaintext NIK but no ciphertext — run cmd/ktp-backfill before migrating', leak_count;
    END IF;
END $$;

ALTER TABLE crm.leads     DROP COLUMN nik;
ALTER TABLE crm.customers DROP COLUMN nik;

COMMIT;
