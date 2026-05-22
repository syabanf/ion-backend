-- M7 hardening — KTP encryption at rest.
--
-- Adds `nik_encrypted bytea` columns to crm.leads and crm.customers.
-- Application code (pkg/cryptutil + AES-256-GCM) populates the new
-- column on every write going forward. The existing plaintext `nik`
-- column stays for one rollout cycle so reads can fall back during
-- backfill; a follow-up migration drops it once cmd/ktp-backfill has
-- migrated existing rows.
--
-- Indexing: encrypted-at-rest means we can't do exact-match lookups
-- via the encrypted bytes. If lead/customer "find by NIK" becomes a
-- production workflow we'll add a side `nik_hmac` column for blind
-- equality search. Today no API surface searches by NIK.

BEGIN;

ALTER TABLE crm.leads
    ADD COLUMN IF NOT EXISTS nik_encrypted BYTEA;

ALTER TABLE crm.customers
    ADD COLUMN IF NOT EXISTS nik_encrypted BYTEA;

COMMIT;
