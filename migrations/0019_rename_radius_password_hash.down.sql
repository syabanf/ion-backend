BEGIN;

ALTER TABLE network.radius_accounts
    RENAME COLUMN password_hash TO password_encrypted;

COMMIT;
