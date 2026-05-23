-- =====================================================================
-- Down migration for 0070_wave105_audit_append_only.up.sql
--
-- Drops:
--   * append-only enforcement trigger + function
--   * hash-chain BEFORE INSERT trigger + function
--   * compute_audit_hash function
--   * prev_hash + row_hash columns
--   * idx_audit_logs_timestamp_desc index
--
-- Role grants: NOT restored — the migration revoked UPDATE/DELETE
-- from PUBLIC, which is the default before the up ran on a fresh
-- schema as well (the 0001 platform_core table never explicitly
-- GRANT'd them to PUBLIC). Restoring would require re-granting
-- privileges that aren't part of any documented role contract.
-- If a future migration needs them back, add an explicit
-- GRANT UPDATE, DELETE ON identity.audit_logs TO ion_app then.
-- =====================================================================

DROP TRIGGER IF EXISTS enforce_audit_append_only ON identity.audit_logs;
DROP FUNCTION IF EXISTS identity.audit_append_only();

DROP TRIGGER IF EXISTS audit_chain_bi ON identity.audit_logs;
DROP FUNCTION IF EXISTS identity.audit_chain_bi();
DROP FUNCTION IF EXISTS identity.compute_audit_hash(UUID, TEXT, JSONB);

DROP INDEX IF EXISTS identity.idx_audit_logs_timestamp_desc;

ALTER TABLE identity.audit_logs DROP COLUMN IF EXISTS row_hash;
ALTER TABLE identity.audit_logs DROP COLUMN IF EXISTS prev_hash;

-- pgcrypto extension intentionally left in place — other migrations
-- may depend on it (it's a no-op if already used elsewhere).
