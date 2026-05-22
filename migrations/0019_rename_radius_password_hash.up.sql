-- M7 hardening — rename radius_accounts.password_encrypted → password_hash.
--
-- The column has always stored bcrypt(plaintext), not encrypted bytes.
-- The misnomer was flagged in the May 2026 audit (see the comment on
-- domain.RadiusAccount); we deferred the rename behind audit-consumer
-- + replication coordination. This migration is the cut-over.
--
-- Single ALTER (cheap; pg renames a column without rewriting data).
-- The application code is updated in lock-step to read/write the new
-- name. There is no overlap window where both names exist — pg DDL
-- is transactional, so the rename + the binary restart land together.

BEGIN;

ALTER TABLE network.radius_accounts
    RENAME COLUMN password_encrypted TO password_hash;

COMMIT;
