-- 0027_enterprise_quotation.up.sql
--
-- Phase 4a — Quotation (PDF artifact). Generated automatically when a
-- BOQ flips to boq_approved; re-generated as v(N+1) when a Negotiation
-- (Phase 4b) completes against the same BOQ.
--
-- Storage strategy: PDF bytes live in the DB (bytea). At MVP scale a
-- quote is ~10–50KB; even 10k quotes is <500MB. We get atomic writes,
-- single backup target, and no separate object-store wiring. When the
-- catalog of quotes grows past a million we switch to S3-style URLs
-- and the column becomes a redirect target — the API contract stays
-- the same.
--
-- Hash is SHA-256 of the PDF bytes (TC-QT-002 verification). Stored
-- alongside so a client can verify the artifact wasn't tampered with
-- in transit.

BEGIN;

CREATE TABLE enterprise.quotations (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- Shared across versions of the same logical quote; (number, version)
    -- pair is unique below.
    quotation_number    TEXT NOT NULL,
    version_no          INT NOT NULL DEFAULT 1 CHECK (version_no >= 1),
    -- Lock to the BOQ version that was approved (or negotiated). The
    -- BOQ is itself a versioned, immutable artifact, so this FK is
    -- the integrity anchor for "what was quoted to the client."
    boq_version_id      UUID NOT NULL
        REFERENCES enterprise.boq_versions(id) ON DELETE RESTRICT,
    -- Opportunity convenience FK (denormalized so list queries don't
    -- need a join through boq_versions).
    opportunity_id      UUID NOT NULL
        REFERENCES enterprise.opportunities(id) ON DELETE RESTRICT,
    status              TEXT NOT NULL DEFAULT 'issued'
        CHECK (status IN (
            'issued',          -- live, valid_until still in the future
            'expired',         -- valid_until passed
            'superseded',      -- v(N+1) issued; this version is read-only history
            'accepted',        -- customer signed; opportunity advances
            'rejected',        -- customer declined (rare; usually leads to negotiation)
            'cancelled'        -- voided before delivery
        )),
    -- Canonical totals at issuance — copied from BOQ at gen time so
    -- the quotation row is self-describing even if the BOQ row is
    -- later viewed alongside a newer version's totals.
    sell_total          NUMERIC(18,2) NOT NULL DEFAULT 0,
    cost_total          NUMERIC(18,2) NOT NULL DEFAULT 0,
    margin_pct          NUMERIC(6,3) NOT NULL DEFAULT 0,
    currency            CHAR(3) NOT NULL DEFAULT 'IDR',
    -- The PDF artifact + its integrity hash. We use TEXT for hash
    -- (hex SHA-256) and BYTEA for the bytes.
    pdf_bytes           BYTEA NOT NULL,
    pdf_hash            TEXT NOT NULL,    -- 64-char SHA-256 hex
    pdf_bytes_size      INT NOT NULL CHECK (pdf_bytes_size > 0),
    -- Quotation validity window. valid_from defaults to issuance
    -- timestamp; valid_until is configurable per quote (Phase 4
    -- default: 30 days). Expired quotations need a re-quote.
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until         TIMESTAMPTZ NOT NULL,
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accepted_at         TIMESTAMPTZ,
    rejected_at         TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    superseded_at       TIMESTAMPTZ,
    -- Free-text notes for ops context (terms, special clauses).
    notes               TEXT NOT NULL DEFAULT '',
    -- Optimistic concurrency.
    revision            INT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    issued_by           UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (quotation_number, version_no)
);

CREATE INDEX idx_quotations_boq ON enterprise.quotations (boq_version_id);
CREATE INDEX idx_quotations_opp ON enterprise.quotations (opportunity_id);
CREATE INDEX idx_quotations_status ON enterprise.quotations (status);
-- Composite for the "live" quote lookup — we want the most recent
-- issued version per quotation_number.
CREATE INDEX idx_quotations_number_version
    ON enterprise.quotations (quotation_number, version_no DESC);

CREATE TRIGGER trg_quotations_touch
    BEFORE UPDATE ON enterprise.quotations
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- RBAC permissions
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'quotation.read',     'View quotations + download PDFs'),
    ('enterprise', 'quotation.manage',   'Generate / cancel / accept-on-customer-behalf')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket grant for the new perms.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN ('quotation.read', 'quotation.manage')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager', 'sales_rep')
  AND p.module = 'enterprise'
  AND p.action = 'quotation.read'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'enterprise'
  AND p.action = 'quotation.manage'
ON CONFLICT DO NOTHING;

COMMIT;
