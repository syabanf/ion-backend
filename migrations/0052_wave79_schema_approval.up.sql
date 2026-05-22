-- 0052 — Wave 79: Schema approval workflow
--
-- Closes TC-SCH-007 ("Submit for Approval sends to all approvers"),
-- TC-SCH-008 ("Publish only enabled after all approvers accept"),
-- TC-SCH-009 ("Reject returns to Draft with reason"), TC-SCH-010
-- ("Publish creates new version while preserving prior"), and
-- TC-RBAC-014 ("approver role configurable per schema kind").
--
-- Schema lifecycle prior to this migration:
--   draft → published → superseded
--
-- After this migration:
--   draft → submitted → approved → published → superseded
--                   ↘ rejected (back to draft with reason captured)
--
-- The Publish endpoint refuses to flip a schema unless every
-- configured approver role has voted yes for the version.

BEGIN;

-- ============================================================
-- Extend schema_definitions.status enum + capture rejection reason
-- ============================================================

-- The CHECK constraint name on schema_definitions.status depends on
-- the version of postgres + earlier migrations. We drop by lookup so
-- this migration works against both production (where the constraint
-- exists from 0032) and any environment that may have already
-- evolved it.
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
        CHECK (status IN ('draft','submitted','approved','published','rejected','superseded'));

ALTER TABLE platform.schema_definitions
    ADD COLUMN IF NOT EXISTS rejection_reason TEXT,
    ADD COLUMN IF NOT EXISTS submitted_at     TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS approved_at      TIMESTAMPTZ;

-- ============================================================
-- schema_approvers — per-kind config of required approver roles
-- ============================================================
--
-- Each (kind, role_code) row means "publishing a schema of this kind
-- requires a positive vote from a user with this role". Multiple
-- approvers per kind are AND-ed (all must approve). Operations Admin
-- maintains this table via the Admin UI in Wave 81 (out of scope here).

CREATE TABLE IF NOT EXISTS platform.schema_approvers (
    schema_kind  TEXT NOT NULL,
    role_code    TEXT NOT NULL,
    required     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (schema_kind, role_code)
);

-- Seed sensible defaults so the workflow works out-of-the-box:
--   billing      → finance_manager
--   commission   → finance_manager + sales_manager
--   suspension   → finance_manager + operations_admin
--   service      → product_admin
--
-- These can be edited by Ops Admin in Wave 81; seed via INSERT … ON
-- CONFLICT DO NOTHING so re-running the migration is safe.
INSERT INTO platform.schema_approvers (schema_kind, role_code, required) VALUES
    ('billing',    'finance_manager',  TRUE),
    ('commission', 'finance_manager',  TRUE),
    ('commission', 'sales_manager',    TRUE),
    ('suspension', 'finance_manager',  TRUE),
    ('suspension', 'operations_admin', TRUE),
    ('service',    'product_admin',    TRUE)
ON CONFLICT (schema_kind, role_code) DO NOTHING;

-- ============================================================
-- schema_approvals — actual votes cast against a schema version
-- ============================================================

CREATE TABLE IF NOT EXISTS platform.schema_approvals (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    schema_version_id UUID NOT NULL REFERENCES platform.schema_definitions(id) ON DELETE CASCADE,
    approver_user_id  UUID NOT NULL,
    approver_role     TEXT NOT NULL,
    decision          TEXT NOT NULL CHECK (decision IN ('approve','reject')),
    reason            TEXT,
    decided_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- One vote per (version, role) — re-voting overwrites via the
    -- UPDATE path in the usecase.
    UNIQUE (schema_version_id, approver_role)
);

CREATE INDEX IF NOT EXISTS idx_schema_approvals_version
    ON platform.schema_approvals (schema_version_id);

COMMIT;
