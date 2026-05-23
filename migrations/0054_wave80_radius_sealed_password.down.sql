ALTER TABLE network.radius_accounts
    DROP COLUMN IF EXISTS password_key_version,
    DROP COLUMN IF EXISTS password_sealed;
