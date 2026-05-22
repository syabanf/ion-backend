-- Reverse of 0052_wave79_schema_approval.up.sql.

BEGIN;

DROP INDEX IF EXISTS platform.idx_schema_approvals_version;
DROP TABLE IF EXISTS platform.schema_approvals;
DROP TABLE IF EXISTS platform.schema_approvers;

ALTER TABLE platform.schema_definitions
    DROP COLUMN IF EXISTS approved_at,
    DROP COLUMN IF EXISTS submitted_at,
    DROP COLUMN IF EXISTS rejection_reason;

-- Restore the pre-Wave-79 status constraint.
DO $$
DECLARE
    cname text;
BEGIN
    SELECT con.conname INTO cname
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
    WHERE nsp.nspname = 'platform'
      AND rel.relname = 'schema_definitions'
      AND con.contype = 'c'
      AND pg_get_constraintdef(con.oid) LIKE '%status%';
    IF cname IS NOT NULL THEN
        EXECUTE 'ALTER TABLE platform.schema_definitions DROP CONSTRAINT ' || quote_ident(cname);
    END IF;
END$$;

ALTER TABLE platform.schema_definitions
    ADD CONSTRAINT schema_definitions_status_check
        CHECK (status IN ('draft','published','superseded'));

COMMIT;
