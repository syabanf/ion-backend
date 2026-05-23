-- Wave 100 — Partnership bounded context (Phase 1 Enterprise).
--
-- Scope: Partnership Monthly Submission (10 TCs) + Partnership Settlement
-- (8 TCs) + Monthly Compliance Check (16 TCs). All three modules share
-- the new `partnership` schema and live behind their own bounded
-- context (`internal/partnership`, `cmd/partnership-svc`). The schema
-- never reaches into reseller.*, enterprise.*, or tax.* — cross-context
-- references (reseller_account_id, signed_by, submitted_by) are stored
-- as plain UUIDs and resolved by the calling service at display time.
-- This keeps the future extraction into a standalone microservice
-- trivial (same playbook as Wave 94 reseller bounded context).
--
-- Lifecycle summary (PRD §Partnership Monthly Submission §3):
--   draft → submitted → confirmed       (settlement issued on confirm)
--                    ↘ returned → draft (Finance Review return-with-comment)
--   draft|returned → cancelled          (terminal)
--
-- Settlement lifecycle:
--   pending → approved → paid (terminal)
--          ↘ cancelled        (also from approved)
--
-- Compliance lifecycle (per evaluation row):
--   ramp_skipped | passed | breached
--   (no transitions; each (reseller, year, month) gets one row)

BEGIN;

CREATE SCHEMA IF NOT EXISTS partnership;

-- =====================================================================
-- agreements — per-reseller revenue-share + compliance contract
-- =====================================================================
--
-- terms_json carries the full agreement payload (target_net_revenue,
-- payment_terms, support_level, etc.) — kept as JSONB so the contract
-- shape can evolve without per-revision migrations. The columns hoisted
-- out are the ones the settlement formula + compliance evaluator read
-- on the hot path; everything else stays in terms_json.
--
-- revshare_pct is stored as NUMERIC(5,4) so 0.3000 = 30%. The
-- settlement formula multiplies it directly by net_revenue.
--
-- ramp_months is the per-agreement grace window where compliance
-- evaluations are skipped (status='ramp_skipped'). Default 2 months —
-- the first two confirmed submissions don't count against the
-- compliance threshold.
--
-- compliance_threshold_pct: 0..1, e.g. 0.80 = 80% of target_net_revenue.
-- The evaluator computes achieved = submission.net_revenue / target;
-- achieved >= threshold → passed, else breached.
--
-- effective_from / effective_to is an open-ended date range. The lookup
-- "active agreement at date X" uses (effective_from <= X AND
-- (effective_to IS NULL OR effective_to >= X)).
CREATE TABLE partnership.agreements (
    id                        UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id       UUID NOT NULL,
    terms_json                JSONB NOT NULL,
    revshare_pct              NUMERIC(5,4) NOT NULL DEFAULT 0.30,
    ramp_months               INTEGER NOT NULL DEFAULT 2,
    compliance_threshold_pct  NUMERIC(5,4) NOT NULL DEFAULT 0.80,
    effective_from            DATE NOT NULL,
    effective_to              DATE,
    signed_by                 UUID,
    signed_at                 TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_partnership_agreements_reseller
    ON partnership.agreements (reseller_account_id, effective_from DESC);

-- =====================================================================
-- monthly_submissions — reseller submits revenue figures per month
-- =====================================================================
--
-- UNIQUE (reseller_account_id, period_year, period_month) enforces "one
-- submission per reseller-month" — re-submission after a 'returned'
-- ruling flips the same row back to draft rather than inserting a new
-- one. This also keeps the cron compliance evaluator deterministic.
--
-- evidence_url + evidence_hash carry the supporting document (CSV
-- export, screenshot bundle, etc.). The hash is sha256(content) at
-- store time so we can detect post-submission tampering.
--
-- Status timestamps mirror the lifecycle: submitted_at fires on the
-- draft→submitted flip, confirmed_at on submitted→confirmed,
-- returned_at on submitted→returned. The state machine in
-- domain/monthly_submission.go enforces the transitions; the DB only
-- enforces the enum values via CHECK.
CREATE TABLE partnership.monthly_submissions (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agreement_id        UUID NOT NULL REFERENCES partnership.agreements(id) ON DELETE RESTRICT,
    reseller_account_id UUID NOT NULL,
    period_year         INTEGER NOT NULL,
    period_month        INTEGER NOT NULL CHECK (period_month BETWEEN 1 AND 12),
    status              TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','submitted','confirmed','returned','cancelled')),
    gross_revenue       NUMERIC(18,2),
    net_revenue         NUMERIC(18,2),
    subscriber_count    INTEGER,
    churn_count         INTEGER,
    evidence_url        TEXT,
    evidence_hash       TEXT,
    submitted_by        UUID,
    submitted_at        TIMESTAMPTZ,
    confirmed_by        UUID,
    confirmed_at        TIMESTAMPTZ,
    returned_reason     TEXT,
    returned_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (reseller_account_id, period_year, period_month)
);

CREATE INDEX idx_partnership_submissions_status_period
    ON partnership.monthly_submissions (status, period_year, period_month);

-- =====================================================================
-- settlements — one per confirmed submission
-- =====================================================================
--
-- agreement_terms_snapshot freezes the agreement payload at confirm time
-- so later edits to partnership.agreements never retroactively rewrite
-- a closed settlement (TC-PS-005). Same idea as Wave 95's
-- customer_po → quotation snapshot.
--
-- formula_hash is sha256 of the canonical formula inputs
-- (gross|net|revshare_pct|tax|payable|agreement_id|period). If any of
-- those are mutated downstream (which they shouldn't be), the hash
-- comparison flags the row as tampered. Computed in
-- domain/settlement.go ComputeFormulaHash().
--
-- pdf_url + pdf_hash carry the generated settlement PDF. The PDF
-- generator stub (port.SettlementPDFGenerator) returns a placeholder
-- byte stream — Wave 100b can swap in a real PDF library without
-- changing the schema.
CREATE TABLE partnership.settlements (
    id                        UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    submission_id             UUID NOT NULL UNIQUE
        REFERENCES partnership.monthly_submissions(id) ON DELETE RESTRICT,
    agreement_id              UUID NOT NULL,
    agreement_terms_snapshot  JSONB NOT NULL,
    gross_revenue             NUMERIC(18,2) NOT NULL,
    net_revenue               NUMERIC(18,2) NOT NULL,
    revshare_amount           NUMERIC(18,2) NOT NULL,
    tax_amount                NUMERIC(18,2) NOT NULL DEFAULT 0,
    payable_amount            NUMERIC(18,2) NOT NULL,
    formula_hash              TEXT NOT NULL,
    status                    TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','approved','paid','cancelled')),
    pdf_url                   TEXT,
    pdf_hash                  TEXT,
    approved_by               UUID,
    approved_at               TIMESTAMPTZ,
    paid_at                   TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_partnership_settlements_status
    ON partnership.settlements (status, created_at DESC);

-- =====================================================================
-- compliance_evaluations — one row per (reseller, year, month) from cron
-- =====================================================================
--
-- The monthly evaluator (cron.MonthlyComplianceEvaluator) computes one
-- row per reseller-with-active-agreement per closed calendar month.
-- UNIQUE (reseller_account_id, period_year, period_month) makes the
-- evaluator idempotent — re-running the cron on the same period is a
-- no-op rather than producing duplicate rows.
--
-- status:
--   ramp_skipped — within agreement.ramp_months from first submission
--   passed       — achieved >= compliance_threshold_pct
--   breached     — achieved <  compliance_threshold_pct
--                  (reason carries "achieved X% < threshold Y%")
CREATE TABLE partnership.compliance_evaluations (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reseller_account_id UUID NOT NULL,
    agreement_id        UUID NOT NULL,
    period_year         INTEGER NOT NULL,
    period_month        INTEGER NOT NULL,
    threshold_pct       NUMERIC(5,4) NOT NULL,
    achieved_pct        NUMERIC(5,4) NOT NULL,
    status              TEXT NOT NULL
        CHECK (status IN ('ramp_skipped','passed','breached')),
    reason              TEXT,
    evaluated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (reseller_account_id, period_year, period_month)
);

CREATE INDEX idx_partnership_compliance_status
    ON partnership.compliance_evaluations (status, evaluated_at DESC);

-- =====================================================================
-- Demo seed — one agreement for the Wave 94 demo reseller.
-- =====================================================================
--
-- Wave 94 seeded reseller_account 00000000-0000-0000-0000-000000009401
-- in migration 0062. We attach a default agreement so the smoke test
-- can post a submission immediately without a separate "create
-- agreement" step. The fixed UUID is intentional so the seed is
-- idempotent across env resets.
INSERT INTO partnership.agreements
    (id, reseller_account_id, terms_json,
     revshare_pct, ramp_months, compliance_threshold_pct,
     effective_from, signed_at)
VALUES (
    '00000000-0000-0000-0000-000000010001',
    '00000000-0000-0000-0000-000000009401',
    '{
      "target_net_revenue": 50000000,
      "payment_terms": "net_30",
      "support_level": "standard",
      "notes": "Wave 100 demo agreement; default 30% revshare, 80% threshold, 2-month ramp"
    }'::jsonb,
    0.3000,
    2,
    0.8000,
    DATE '2025-01-01',
    NOW()
)
ON CONFLICT (id) DO NOTHING;

-- =====================================================================
-- Permission catalog — 9 new permissions under module 'partnership'.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('partnership', 'agreement.read',       'View partnership agreements'),
    ('partnership', 'agreement.write',      'Create/edit partnership agreements'),
    ('partnership', 'submission.read',      'View monthly submissions'),
    ('partnership', 'submission.write',     'Draft/edit/cancel monthly submissions'),
    ('partnership', 'submission.confirm',   'Confirm or return monthly submissions (Finance gate)'),
    ('partnership', 'settlement.read',      'View settlements'),
    ('partnership', 'settlement.approve',   'Approve and mark settlements paid'),
    ('partnership', 'compliance.read',      'View compliance evaluations')
ON CONFLICT (module, action) DO NOTHING;

-- =====================================================================
-- Role catalog — finance_admin already exists (0002_auth_rbac); add
-- compliance_admin if missing. Both are bundles, not column changes.
-- =====================================================================
INSERT INTO identity.roles (name, description) VALUES
    ('finance_admin',     'Finance team: invoice/settlement/commission'),
    ('compliance_admin',  'Compliance officer: monthly evaluation review')
ON CONFLICT (name) DO NOTHING;

-- =====================================================================
-- Role → permission grants.
-- =====================================================================

-- super_admin gets all partnership permissions (matches the existing
-- "super_admin → every permission" cross-join from 0002, which only
-- fires for permissions that existed at that migration time).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'partnership'
ON CONFLICT DO NOTHING;

-- finance_admin: settlement.* + agreement.read + submission.read.
-- The settlement approve/mark-paid path is finance's domain; agreements
-- + submissions are read-only context for them.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_admin'
  AND p.module = 'partnership'
  AND p.action IN ('settlement.read','settlement.approve','agreement.read','submission.read','submission.confirm')
ON CONFLICT DO NOTHING;

-- compliance_admin: compliance.read + agreement.read. They read the
-- evaluator output but don't approve settlements or sign agreements.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'compliance_admin'
  AND p.module = 'partnership'
  AND p.action IN ('compliance.read','agreement.read')
ON CONFLICT DO NOTHING;

COMMIT;
