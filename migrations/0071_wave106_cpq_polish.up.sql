-- =====================================================================
-- Migration 0071 — Wave 106 — CPQ flow polish
--
-- Rolls in the residual schema additions from the Wave 91 audit's
-- Pricebook / Opportunity / BOQ / Negotiation gaps:
--
--   1. enterprise.pricebook_lines.priority_score (TC-PB-010 — internal
--      vendor priority badge per pricebook line). Default 0; the line
--      list endpoint sorts by (priority_score DESC, sku ASC) when
--      ?sort=priority is set.
--
--   2. enterprise.pre_boq_required_fields — admin-config table that
--      drives the Pre-BOQ structured validator (TC-OP-009). The
--      Opportunity.CompletePreBOQ usecase parses the incoming JSON and
--      asserts every required=TRUE row's field_key is present + non-
--      empty. Seeded with the 5 canonical fields (customer_name,
--      customer_email, contact_phone, address_line, expected_capacity_mbps).
--
--   3. enterprise.boq_versions.commercial_owner_subsidiary_id (TC-BQ-013) —
--      nullable FK to subsidiaries; when assigned_provider_company_id on a
--      line differs from this column, the DTO mapper sets ic_po_required=TRUE.
--      Nullable for backward compat — Wave 92 deferred this to Wave 106.
--
-- Contract:
--   - All three additions are nil-safe at the application layer; the BOQ
--     read path, the Pre-BOQ validator, and the line-priority sorter all
--     fall back to "no change in behaviour" when the column is NULL/empty.
-- =====================================================================

-- ---------------------------------------------------------------------
-- 1. pricebook_lines.priority_score
-- ---------------------------------------------------------------------
ALTER TABLE enterprise.pricebook_lines
    ADD COLUMN IF NOT EXISTS priority_score INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_pricebook_lines_priority
    ON enterprise.pricebook_lines (pricebook_id, priority_score DESC, sku ASC);

-- ---------------------------------------------------------------------
-- 2. pre_boq_required_fields config table + seed
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS enterprise.pre_boq_required_fields (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    field_key   TEXT NOT NULL UNIQUE,
    label       TEXT NOT NULL,
    field_type  TEXT NOT NULL DEFAULT 'string',
    required    BOOLEAN NOT NULL DEFAULT TRUE,
    position    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pre_boq_required_fields_position
    ON enterprise.pre_boq_required_fields (position ASC);

-- Seed the 5 canonical fields per the Wave 91 catalog TC-OP-009.
-- ON CONFLICT keeps the migration idempotent — re-running won't dup.
INSERT INTO enterprise.pre_boq_required_fields (field_key, label, field_type, required, position) VALUES
    ('customer_name',           'Customer Name',           'string', TRUE, 1),
    ('customer_email',          'Customer Email',          'email',  TRUE, 2),
    ('contact_phone',           'Contact Phone',           'string', TRUE, 3),
    ('address_line',            'Address Line',            'string', TRUE, 4),
    ('expected_capacity_mbps',  'Expected Capacity (Mbps)', 'number', TRUE, 5)
ON CONFLICT (field_key) DO NOTHING;

-- ---------------------------------------------------------------------
-- 3. boq_versions.commercial_owner_subsidiary_id
-- ---------------------------------------------------------------------
ALTER TABLE enterprise.boq_versions
    ADD COLUMN IF NOT EXISTS commercial_owner_subsidiary_id UUID
        REFERENCES enterprise.subsidiaries(id);

CREATE INDEX IF NOT EXISTS idx_boq_versions_commercial_owner
    ON enterprise.boq_versions (commercial_owner_subsidiary_id)
    WHERE commercial_owner_subsidiary_id IS NOT NULL;
