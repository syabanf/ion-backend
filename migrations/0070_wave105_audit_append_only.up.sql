-- =====================================================================
-- Migration 0070 — Wave 105 — audit_logs append-only + hash chain
--
-- Rolls in two pieces from the Wave 91 audit doc:
--
--   1. Append-only DB enforcement (TC-AU-001):
--      * BEFORE UPDATE / BEFORE DELETE trigger raises an exception so
--        a compromised app role can't tamper with history.
--      * UPDATE + DELETE grants revoked from PUBLIC so even a direct
--        psql session as the app role can't bypass the trigger by
--        going around it.
--
--   2. Hash chain (TC-AU-008):
--      * `prev_hash` + `row_hash` columns
--      * compute_audit_hash(id, prev_hash, payload) function
--        returns sha256-hex(prev_hash || '|' || payload::text)
--      * BEFORE INSERT trigger fills both from the latest row's
--        row_hash + the incoming payload jsonb.
--
-- Contract (also documented in docs/wave-105-perf-baseline.md):
--   - Given any audit_logs row, its row_hash MUST equal
--     compute_audit_hash(id, prev_hash, payload). Tampering breaks
--     the chain.
--   - prev_hash for the first ever inserted row is ''.
--   - Existing rows pre-Wave-105 keep prev_hash='' + row_hash='' —
--     the chain starts from new inserts forward (back-fill would
--     require choosing an ordering and is best-effort only).
-- =====================================================================

-- ---------------------------------------------------------------------
-- 1a. New columns. NOT NULL with default '' so existing rows stay
--     valid without a separate UPDATE pass.
-- ---------------------------------------------------------------------
ALTER TABLE identity.audit_logs
    ADD COLUMN IF NOT EXISTS prev_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE identity.audit_logs
    ADD COLUMN IF NOT EXISTS row_hash  TEXT NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------
-- 1b. The audit_logs schema uses scalar columns (field_changed, before/
--     after) rather than a single jsonb payload. To compute a stable
--     hash we serialize the meaningful subset into a jsonb_build_object
--     in the BEFORE INSERT trigger. A dedicated `payload` column would
--     be cleaner but would duplicate state already in the row — keeping
--     the build inline avoids the duplication.
-- ---------------------------------------------------------------------

-- ---------------------------------------------------------------------
-- 2. compute_audit_hash(id, prev_hash, payload) — pure, deterministic
--    sha256-hex of (prev_hash || '|' || payload::text). Marked IMMUTABLE
--    so the planner can fold it.
-- ---------------------------------------------------------------------
CREATE OR REPLACE FUNCTION identity.compute_audit_hash(
    audit_id  UUID,
    prev_hash TEXT,
    payload   JSONB
) RETURNS TEXT
LANGUAGE sql IMMUTABLE AS $$
    SELECT encode(
        digest(
            COALESCE(prev_hash, '') || '|' || payload::text,
            'sha256'
        ),
        'hex'
    )
$$;

-- The digest() function lives in pgcrypto. Enable it if not already on
-- (CI's postgis image bundles it; running this on a stock postgres
-- needs the extension installed).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------
-- 3. BEFORE INSERT — populate prev_hash + row_hash on every new row.
--    The LATERAL-style "ORDER BY timestamp DESC LIMIT 1" lookup of the
--    prior tail is read against the SAME table, which is allowed inside
--    a row-level trigger (the new row isn't visible yet because the
--    trigger fires BEFORE insert).
-- ---------------------------------------------------------------------
CREATE OR REPLACE FUNCTION identity.audit_chain_bi() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prev TEXT := '';
    payload JSONB;
BEGIN
    -- Look up the most recent existing row_hash, ordered by timestamp
    -- DESC + id (tiebreaker for identical timestamps). The idx_audit_
    -- logs_timestamp_desc index below makes this O(log n).
    SELECT row_hash INTO prev
    FROM identity.audit_logs
    ORDER BY timestamp DESC, id DESC
    LIMIT 1;

    IF prev IS NULL THEN
        prev := '';
    END IF;

    -- Build the canonical payload for hashing. Order matters for
    -- determinism — we serialize a fixed key set so column-rename
    -- migrations don't silently break verification. record_id is
    -- the polymorphic foreign key (text); ts is the row's audit time.
    payload := jsonb_build_object(
        'user_id',       NEW.user_id,
        'module',        NEW.module,
        'record_type',   NEW.record_type,
        'record_id',     NEW.record_id,
        'field_changed', NEW.field_changed,
        'before_value',  NEW.before_value,
        'after_value',   NEW.after_value,
        'reason',        NEW.reason,
        'timestamp',     NEW.timestamp
    );

    NEW.prev_hash := prev;
    NEW.row_hash  := identity.compute_audit_hash(NEW.id, prev, payload);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS audit_chain_bi ON identity.audit_logs;
CREATE TRIGGER audit_chain_bi
BEFORE INSERT ON identity.audit_logs
FOR EACH ROW
EXECUTE FUNCTION identity.audit_chain_bi();

-- The chain lookup needs ORDER BY timestamp DESC LIMIT 1 to be fast.
-- We already index (user_id, timestamp DESC), but a pure timestamp
-- DESC index is the one the trigger uses.
CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp_desc
    ON identity.audit_logs (timestamp DESC, id DESC);

-- ---------------------------------------------------------------------
-- 4. Append-only enforcement — block UPDATE + DELETE at the trigger
--    layer. The trigger fires for EVERY user (including the app role
--    and the migration role). Roll-forward migrations can drop the
--    trigger if they ever need to alter the schema; ops auditors verify
--    by SELECTing pg_trigger.
-- ---------------------------------------------------------------------
CREATE OR REPLACE FUNCTION identity.audit_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log: append-only enforced (no UPDATE/DELETE allowed on identity.audit_logs)'
        USING ERRCODE = '42501';  -- insufficient_privilege
END;
$$;

DROP TRIGGER IF EXISTS enforce_audit_append_only ON identity.audit_logs;
CREATE TRIGGER enforce_audit_append_only
BEFORE UPDATE OR DELETE ON identity.audit_logs
FOR EACH ROW
EXECUTE FUNCTION identity.audit_append_only();

-- ---------------------------------------------------------------------
-- 5. Role-level revoke. The app role connecting to the DB (typically
--    `ion_app` in prod, `ci` in CI) must lose UPDATE + DELETE on
--    identity.audit_logs so trigger-bypass via DDL-rollback isn't
--    available. We revoke from PUBLIC defensively — any role that
--    inherits PUBLIC loses the privilege, which matches the deny-by-
--    default contract.
--
-- Best-effort: in some environments the app role is also the superuser
-- (notably local dev + CI), in which case the REVOKE is a no-op
-- because superuser bypasses GRANT checks anyway. The trigger above is
-- the load-bearing protection.
-- ---------------------------------------------------------------------
REVOKE UPDATE, DELETE ON identity.audit_logs FROM PUBLIC;
