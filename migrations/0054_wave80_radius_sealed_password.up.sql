-- Wave 80 phase 1 — reversible RADIUS password storage scaffold.
--
-- FreeRADIUS CHAP needs the *plaintext* password to compute the
-- response; bcrypt is one-way and can't serve it. We add two columns
-- so the existing bcrypt rows keep working while new rows can also
-- carry an AES-GCM-sealed plaintext:
--
--   password_sealed       BYTEA — AES-GCM(nonce||ciphertext||tag)
--   password_key_version  INT   — which RADIUS_PWD_KEY_V<n> opened it
--
-- The protocol bridge itself (FreeRadiusClient) ships as a stub in
-- Wave 80 phase 1 and graduates to real CoA packets in phase 2 once
-- the layeh.com/radius dep + mock RADIUS server land in CI.
--
-- Both columns are nullable so legacy rows continue to load. The
-- LocalRadiusClient writes both `password_hash` (legacy bcrypt) and
-- `password_sealed` (new) when a Sealer is wired; reads prefer
-- sealed when available, falling back to bcrypt for legacy.
--
-- key_version starts at 1; rotating keys (KEK rotation) bumps it.
-- The runtime carries a small keyring; rows with older versions can
-- still be decrypted because each key stays in the keyring until all
-- rows are re-sealed.

ALTER TABLE network.radius_accounts
    ADD COLUMN IF NOT EXISTS password_sealed       BYTEA,
    ADD COLUMN IF NOT EXISTS password_key_version  INT;
