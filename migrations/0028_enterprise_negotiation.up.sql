-- 0028_enterprise_negotiation.up.sql
--
-- Phase 4b — Negotiation. Customer rejects a quotation; Sales Manager
-- activates a negotiation; VP Sales submits pricing rounds; chain
-- approves; completed negotiation triggers a re-quote (Phase 4a's
-- GenerateQuotation hook already handles v(N+1)).
--
-- Three new entities + one extension to boq_versions:
--
--   boq_versions.* (additive columns)
--     - negotiation_enabled
--     - negotiation_type
--     - negotiation_mode (sequential | parallel)
--     - pricing_adjustment_allowed
--     - negotiation_margin_floor (BR-6)
--     - negotiation_discount_ceiling
--     - negotiation_config_locked_at (TC-NEG-002)
--
--   enterprise.negotiation_participants
--     - one row per approver in the chain (user_id + step_no + role_tag)
--
--   enterprise.negotiations
--     - one per BOQ; lifecycle: inactive → active → completed | aborted
--
--   enterprise.negotiation_rounds
--     - one per VP price submission; carries the before/after price
--       changes as jsonb + the round-level approval chain state
--
--   enterprise.negotiation_round_approvals
--     - per-step approval instance for a round (mirrors
--       approval_instances but scoped to negotiation rounds; kept
--       separate so the table doesn't become polymorphic)

BEGIN;

-- =====================================================================
-- BOQ extension — negotiation config columns
-- =====================================================================
ALTER TABLE enterprise.boq_versions
    ADD COLUMN IF NOT EXISTS negotiation_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS negotiation_type TEXT NOT NULL DEFAULT 'standard'
        CHECK (negotiation_type IN ('standard', 'custom')),
    ADD COLUMN IF NOT EXISTS negotiation_mode TEXT NOT NULL DEFAULT 'sequential'
        CHECK (negotiation_mode IN ('sequential', 'parallel')),
    ADD COLUMN IF NOT EXISTS pricing_adjustment_allowed BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS negotiation_margin_floor NUMERIC(6,3) NOT NULL DEFAULT 12.0
        CHECK (negotiation_margin_floor >= 0 AND negotiation_margin_floor <= 100),
    ADD COLUMN IF NOT EXISTS negotiation_discount_ceiling NUMERIC(6,3) NOT NULL DEFAULT 25.0
        CHECK (negotiation_discount_ceiling >= 0 AND negotiation_discount_ceiling <= 100),
    -- Lock timestamp: BOQ approval stamps this and the config becomes
    -- immutable (TC-NEG-002 → HTTP 409 negotiation_config_locked).
    ADD COLUMN IF NOT EXISTS negotiation_config_locked_at TIMESTAMPTZ;

-- =====================================================================
-- Negotiation participants — chain definition per BOQ
--
-- A separate table (vs. text[] of user IDs on the BOQ) so we can:
--   - FK-ish to identity.users for audit consistency
--   - Carry per-member role_tag (VP, Director, CCO) for the auto-inject
--     decision tree
-- =====================================================================
CREATE TABLE enterprise.negotiation_participants (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_version_id  UUID NOT NULL
        REFERENCES enterprise.boq_versions(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL,
    step_no         INT NOT NULL CHECK (step_no >= 1),
    -- role_tag values used by NG-1 / NG-2 / NG-3 logic:
    --   'vp_sales'    — only-pricing-editor (TC-NEG-004)
    --   'director'    — approver
    --   'cco'         — final commercial authority + auto-inject target
    --   other         — informational
    role_tag        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (boq_version_id, step_no, user_id)
);

CREATE INDEX idx_negotiation_participants_boq
    ON enterprise.negotiation_participants (boq_version_id, step_no);

-- =====================================================================
-- Negotiation — top-level lifecycle row, one per BOQ
--
-- Status transitions (TC-SM-NEG-*):
--   inactive → active     (only after a quotation exists for the BOQ)
--   active   → completed  (round chain approves; re-quote fires)
--   active   → aborted    (BOQ revision starts mid-flight, Edge #1)
--   completed/aborted → active: invalid
--
-- We carry only one row per BOQ — multiple rounds nest under this row.
-- =====================================================================
CREATE TABLE enterprise.negotiations (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_version_id  UUID NOT NULL UNIQUE
        REFERENCES enterprise.boq_versions(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'inactive'
        CHECK (status IN ('inactive', 'active', 'completed', 'aborted')),
    activated_at    TIMESTAMPTZ,
    activated_by    UUID,
    completed_at    TIMESTAMPTZ,
    aborted_at      TIMESTAMPTZ,
    abort_reason    TEXT NOT NULL DEFAULT '',
    -- Track the resulting quote version when completion fires the
    -- re-quote hook; nullable until that happens.
    resulting_quotation_id UUID,
    revision        INT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_negotiations_status ON enterprise.negotiations (status);

-- =====================================================================
-- Negotiation rounds — one per VP price submission
--
-- Each round captures the before/after price snapshot as jsonb (the
-- audit trail TC-NEG-014 looks for) plus the round-level margin/
-- discount AT submission time so the auto-inject decision and the
-- post-action guardrail check are auditable.
-- =====================================================================
CREATE TABLE enterprise.negotiation_rounds (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    negotiation_id   UUID NOT NULL
        REFERENCES enterprise.negotiations(id) ON DELETE CASCADE,
    round_no         INT NOT NULL CHECK (round_no >= 1),
    -- Status mirrors the BOQ-approval lifecycle but is round-scoped.
    status           TEXT NOT NULL DEFAULT 'pending_approval'
        CHECK (status IN (
            'pending_approval',   -- chain materialized; approvers acting
            'approved',           -- all steps approved; round committed
            'rejected',           -- any step rejected; round aborted
            'superseded'          -- BOQ revision wiped it out
        )),
    -- Price-change snapshot: array of { line_id, before, after,
    -- before_discount, after_discount } shape. Stored as jsonb so the
    -- shape can evolve without migration.
    price_changes    JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Header-level margin/discount values at submission time. Used by
    -- the auto-inject decision tree (NG-2/NG-3) and the audit trail.
    margin_before    NUMERIC(6,3) NOT NULL,
    margin_after     NUMERIC(6,3) NOT NULL,
    max_discount_after NUMERIC(6,3) NOT NULL DEFAULT 0,
    -- Did the auto-inject fire? Audit-friendly flag (TC-NEG-012).
    cco_auto_injected      BOOLEAN NOT NULL DEFAULT FALSE,
    cco_injection_reason   TEXT NOT NULL DEFAULT ''
        CHECK (cco_injection_reason IN (
            '', 'margin_floor', 'discount_ceiling'
        )),
    -- Submitter (the VP) — captured separately from approval rows.
    submitted_by     UUID,
    submitted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at     TIMESTAMPTZ,    -- set when status flips approved/rejected
    rejection_reason_code TEXT NOT NULL DEFAULT ''
        CHECK (rejection_reason_code IN (
            '', 'pricing', 'scope', 'documentation', 'compliance', 'other'
        )),
    rejection_comment TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (negotiation_id, round_no)
);

CREATE INDEX idx_negotiation_rounds_negotiation
    ON enterprise.negotiation_rounds (negotiation_id, round_no);
CREATE INDEX idx_negotiation_rounds_status
    ON enterprise.negotiation_rounds (status);

-- =====================================================================
-- Round approval instances — per-step approval rows for a round
--
-- Same shape as boq approval_instances but scoped to a negotiation
-- round. Kept separate (vs. polymorphic) so neither table needs a
-- target_type discriminator + the FK relationships stay clean.
-- =====================================================================
CREATE TABLE enterprise.negotiation_round_approvals (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    round_id        UUID NOT NULL
        REFERENCES enterprise.negotiation_rounds(id) ON DELETE CASCADE,
    step_no         INT NOT NULL CHECK (step_no >= 1),
    approver_user_id UUID NOT NULL,
    role_tag        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'superseded_reset')),
    reason_code     TEXT NOT NULL DEFAULT ''
        CHECK (reason_code IN (
            '', 'pricing', 'scope', 'documentation', 'compliance', 'other'
        )),
    comment         TEXT NOT NULL DEFAULT '',
    acted_at        TIMESTAMPTZ,
    acted_at_original TIMESTAMPTZ,
    -- The auto-inject flag is per-step so the audit can show "this
    -- specific CCO step was added by the system, not the template."
    auto_injected   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (round_id, step_no, approver_user_id)
);

CREATE INDEX idx_negotiation_round_approvals_round
    ON enterprise.negotiation_round_approvals (round_id, step_no);
CREATE INDEX idx_negotiation_round_approvals_approver
    ON enterprise.negotiation_round_approvals (approver_user_id, status)
    WHERE status = 'pending';

-- =====================================================================
-- updated_at triggers
-- =====================================================================
CREATE TRIGGER trg_negotiations_touch
    BEFORE UPDATE ON enterprise.negotiations
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_negotiation_rounds_touch
    BEFORE UPDATE ON enterprise.negotiation_rounds
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TRIGGER trg_negotiation_round_approvals_touch
    BEFORE UPDATE ON enterprise.negotiation_round_approvals
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- RBAC permissions
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'negotiation.read',    'View negotiations + rounds'),
    ('enterprise', 'negotiation.configure', 'Set negotiation config on a BOQ draft'),
    ('enterprise', 'negotiation.activate', 'Activate a negotiation on a quotation-issued BOQ'),
    ('enterprise', 'negotiation.submit',  'VP-only — submit a pricing-change round'),
    ('enterprise', 'negotiation.approve', 'Approve / reject negotiation round steps'),
    ('enterprise', 'negotiation.abort',   'Abort an active negotiation')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'negotiation.read', 'negotiation.configure', 'negotiation.activate',
      'negotiation.submit', 'negotiation.approve', 'negotiation.abort'
  )
ON CONFLICT DO NOTHING;

-- sales_manager: full negotiation lifecycle except submit (that's VP).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'enterprise'
  AND p.action IN (
      'negotiation.read', 'negotiation.configure', 'negotiation.activate',
      'negotiation.approve', 'negotiation.abort'
  )
ON CONFLICT DO NOTHING;

-- sales_rep: read-only — context for the deals they own.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'enterprise'
  AND p.action = 'negotiation.read'
ON CONFLICT DO NOTHING;

COMMIT;
