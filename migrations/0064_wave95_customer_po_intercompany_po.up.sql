-- Wave 95 — Customer PO + Intercompany PO foundation.
--
-- Per the Wave 91 Phase 1 Enterprise audit, this wave is the biggest
-- single TC unlock — 58 TCs across Customer PO (10), Intercompany PO
-- (39), and Finance Internal Vendor (10). The data model expected by
-- the catalog:
--
--   Customer PO (B2B2C buyer-facing)
--     ↓ accepted
--   Intercompany PO (auto-drafted per assigned_provider_company_id)
--     ↓ issued → accepted
--   InternalTransaction recognition flips here (was BOQ-approval; the
--   old call site is left LIVE this wave and marked DEPRECATED — a
--   reconciliation cron in Wave 95b will detect double-counting).
--
-- intercompany_pairs is the (commercial_owner, executing) policy table
-- that drives auto-accept: when a pair has auto_accept=true and the
-- IC-PO total is under threshold, AcceptCustomerPO issues + accepts
-- the IC-PO atomically.
--
-- Cross-context references:
--   - opportunity_id / boq_version_id are stored as plain UUIDs (no FK
--     across to enterprise.opportunities / enterprise.boq_versions for
--     now — same pattern as 0030 pre-launch). The application enforces
--     the join via UseCase guard.
--   - commercial_owner_subsidiary_id + executing_subsidiary_id are real
--     FKs to enterprise.subsidiaries (created in 0060).

BEGIN;

-- =====================================================================
-- customer_pos — header table for the customer's signed PO
-- =====================================================================
CREATE TABLE enterprise.customer_pos (
    id                              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    opportunity_id                  UUID NOT NULL,
    boq_version_id                  UUID NOT NULL,
    customer_id                     UUID,
    commercial_owner_subsidiary_id  UUID NOT NULL
        REFERENCES enterprise.subsidiaries(id) ON DELETE RESTRICT,
    po_number                       TEXT NOT NULL,
    po_value                        NUMERIC(18, 2),
    file_url                        TEXT,
    file_hash                       TEXT,
    uploaded_by                     UUID,
    uploaded_at                     TIMESTAMPTZ,
    status                          TEXT NOT NULL DEFAULT 'received'
        CHECK (status IN ('received', 'validated', 'accepted', 'rejected', 'cancelled')),
    validated_at                    TIMESTAMPTZ,
    accepted_at                     TIMESTAMPTZ,
    rejected_at                     TIMESTAMPTZ,
    cancelled_at                    TIMESTAMPTZ,
    rejection_reason                TEXT,
    notes                           TEXT,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_customer_pos_owner_status ON enterprise.customer_pos
    (commercial_owner_subsidiary_id, status);
CREATE INDEX idx_customer_pos_opportunity ON enterprise.customer_pos (opportunity_id);
CREATE INDEX idx_customer_pos_boq_version ON enterprise.customer_pos (boq_version_id);

CREATE TRIGGER trg_customer_pos_touch
    BEFORE UPDATE ON enterprise.customer_pos
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- intercompany_pos — header
-- =====================================================================
CREATE TABLE enterprise.intercompany_pos (
    id                              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_po_id                  UUID NOT NULL
        REFERENCES enterprise.customer_pos(id) ON DELETE CASCADE,
    boq_version_id                  UUID NOT NULL,
    commercial_owner_subsidiary_id  UUID NOT NULL
        REFERENCES enterprise.subsidiaries(id) ON DELETE RESTRICT,
    executing_subsidiary_id         UUID NOT NULL
        REFERENCES enterprise.subsidiaries(id) ON DELETE RESTRICT,
    ic_po_number                    TEXT NOT NULL UNIQUE,
    status                          TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'issued', 'accepted', 'rejected', 'superseded', 'cancelled')),
    total                           NUMERIC(18, 2),
    tax_snapshot_hash               TEXT,
    issued_at                       TIMESTAMPTZ,
    accepted_at                     TIMESTAMPTZ,
    accepted_by                     UUID,
    rejected_at                     TIMESTAMPTZ,
    rejection_reason                TEXT,
    cancelled_at                    TIMESTAMPTZ,
    superseded_at                   TIMESTAMPTZ,
    supersedes_id                   UUID
        REFERENCES enterprise.intercompany_pos(id) ON DELETE SET NULL,
    notes                           TEXT,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ic_pos_executing_status ON enterprise.intercompany_pos
    (executing_subsidiary_id, status);
CREATE INDEX idx_ic_pos_owner_status ON enterprise.intercompany_pos
    (commercial_owner_subsidiary_id, status);
CREATE INDEX idx_ic_pos_boq_version ON enterprise.intercompany_pos (boq_version_id);

CREATE TRIGGER trg_intercompany_pos_touch
    BEFORE UPDATE ON enterprise.intercompany_pos
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- intercompany_po_lines — snapshot of BOQ lines grouped under each IC-PO
-- =====================================================================
CREATE TABLE enterprise.intercompany_po_lines (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ic_po_id            UUID NOT NULL
        REFERENCES enterprise.intercompany_pos(id) ON DELETE CASCADE,
    boq_line_id         UUID,
    sku_or_service_id   UUID,
    description         TEXT,
    qty                 NUMERIC(18, 4),
    unit_price          NUMERIC(18, 4),
    line_total          NUMERIC(18, 2),
    tax_amount          NUMERIC(18, 2),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ic_po_lines_parent ON enterprise.intercompany_po_lines (ic_po_id);

-- =====================================================================
-- intercompany_pairs — auto-accept policy per (commercial_owner, executing)
-- =====================================================================
CREATE TABLE enterprise.intercompany_pairs (
    id                              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    commercial_owner_subsidiary_id  UUID NOT NULL
        REFERENCES enterprise.subsidiaries(id) ON DELETE RESTRICT,
    executing_subsidiary_id         UUID NOT NULL
        REFERENCES enterprise.subsidiaries(id) ON DELETE RESTRICT,
    auto_accept                     BOOLEAN NOT NULL DEFAULT FALSE,
    auto_accept_threshold           NUMERIC(18, 2),
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (commercial_owner_subsidiary_id, executing_subsidiary_id)
);

CREATE TRIGGER trg_intercompany_pairs_touch
    BEFORE UPDATE ON enterprise.intercompany_pairs
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- Demo seed — one pair between the seeded reseller + supplier from 0060
-- with auto_accept=false so the issuing flow is exercised manually
-- in dev.
-- =====================================================================
INSERT INTO enterprise.intercompany_pairs
    (commercial_owner_subsidiary_id, executing_subsidiary_id, auto_accept, auto_accept_threshold)
VALUES
    ('00000000-0000-0000-0000-000000000093',
     '00000000-0000-0000-0000-000000000094',
     FALSE,
     NULL)
ON CONFLICT (commercial_owner_subsidiary_id, executing_subsidiary_id) DO NOTHING;

-- =====================================================================
-- RBAC — permissions + role grants
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'customer_po.read',         'View customer POs'),
    ('enterprise', 'customer_po.write',        'Upload / validate / accept / reject / cancel customer POs'),
    ('enterprise', 'intercompany_po.read',     'View intercompany POs'),
    ('enterprise', 'intercompany_po.write',    'Issue / draft intercompany POs'),
    ('enterprise', 'intercompany_po.accept',   'Accept intercompany POs on the executing side'),
    ('enterprise', 'intercompany_po.reject',   'Reject intercompany POs on the executing side'),
    ('enterprise', 'intercompany_po.cancel',   'Cancel intercompany POs')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin — blanket grant on every permission added this wave
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'customer_po.read', 'customer_po.write',
      'intercompany_po.read', 'intercompany_po.write',
      'intercompany_po.accept', 'intercompany_po.reject',
      'intercompany_po.cancel'
  )
ON CONFLICT DO NOTHING;

-- sales_manager + sales_rep — customer PO full + intercompany PO read
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager', 'sales_rep')
  AND p.module = 'enterprise'
  AND p.action IN (
      'customer_po.read', 'customer_po.write',
      'intercompany_po.read'
  )
ON CONFLICT DO NOTHING;

-- operations_admin — full intercompany PO surface (executing-side ops)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'intercompany_po.read', 'intercompany_po.write',
      'intercompany_po.accept', 'intercompany_po.reject',
      'intercompany_po.cancel'
  )
ON CONFLICT DO NOTHING;

COMMIT;
