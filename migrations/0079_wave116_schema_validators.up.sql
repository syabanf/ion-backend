-- Wave 116 — Deep schema content validators.
--
-- The Go-side validator framework (internal/platform/domain/content_validators.go)
-- is the bulk of this wave; on the database side we only add:
--
--   1. platform.schema_kinds        — registry of known kinds + validator class
--                                     mapping. Decouples the Go validator
--                                     registration from the platform.schema_kind
--                                     ENUM so future kinds (onboarding, reminder,
--                                     late_fee, addon) can land without an
--                                     ALTER TYPE.
--   2. platform.schema_validation_results
--                                   — append-only audit of every validator pass
--                                     (manual trigger + nightly sweep). One row
--                                     per (schema_version_id, run); the latest
--                                     row wins when callers ask "is this schema
--                                     valid?"
--   3. Five demo schema definitions — one per validatable kind (onboarding,
--                                     billing, service, commission, suspension)
--                                     with rich content so the QA scenarios in
--                                     /tmp/p1b-catalog.csv have something to
--                                     resolve against.
--   4. Permission seed                — `platform.schema.validate` for the new
--                                     validate / validate-all / read-validation
--                                     HTTP routes.
--
-- Coordinates with parallel waves: Wave 115 uses migration 0078; Wave 117 uses
-- 0080. We're at 0079, strictly disjoint table-wise (only platform.* touched).
--
-- Down: drops the new tables, the demo seed rows, and the permission seed.

BEGIN;

-- =====================================================================
-- 1. platform.schema_kinds — registry of validatable kinds.
--
-- Why a separate table from the platform.schema_kind ENUM?
--   * The ENUM is referenced by platform.schema_definitions.kind and
--     platform.customer_schema_overrides.schema_kind. Extending it requires
--     an ALTER TYPE + downtime risk we don't need today.
--   * The validator framework only needs a string identifier — so a small
--     registry table is sufficient. Onboarding / reminder / late_fee / addon
--     can land here without ALTER TYPE; their Go validators register
--     against the same string key.
--   * validator_class is informational — the Go-side ValidatorRegistry is
--     the actual dispatch table; this column just lets ops see "what
--     validator runs for kind X" without grepping source.
-- =====================================================================
CREATE TABLE IF NOT EXISTS platform.schema_kinds (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    kind               TEXT NOT NULL UNIQUE,
    label              TEXT NOT NULL,
    content_schema     JSONB NOT NULL DEFAULT '{}'::jsonb,
    validator_class    TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE platform.schema_kinds IS
    'Wave 116 — registry of known schema kinds + their Go validator class. Decoupled from platform.schema_kind ENUM so onboarding/reminder/late_fee/addon can be added without ALTER TYPE.';

INSERT INTO platform.schema_kinds (kind, label, validator_class) VALUES
    ('onboarding', 'Customer Onboarding',                 'OnboardingValidator'),
    ('billing',    'Billing Cycle & Cadence',             'BillingValidator'),
    ('service',    'Service Plan & SLA',                  'ServiceValidator'),
    ('commission', 'Sales Commission & Clawback',         'CommissionValidator'),
    ('suspension', 'Suspension & Restore Cadence',        'SuspensionValidator'),
    ('reminder',   'Invoice Reminder Cadence',            ''),
    ('late_fee',   'Late Fee Policy',                     ''),
    ('addon',      'Add-On Subscription Policy',          '')
ON CONFLICT (kind) DO NOTHING;

-- =====================================================================
-- 2. platform.schema_validation_results — append-only audit of validator runs.
--
-- One row per validator invocation (manual trigger via HTTP + nightly cron
-- sweep). The "latest row per schema_version_id" is the canonical answer to
-- "is this schema valid right now?" — readers use the
-- (schema_version_id, validated_at DESC) index.
--
-- errors / warnings are jsonb arrays of strings so the FE can render them
-- inline; validator_version is the Go-side validator's semver so older
-- results stay attributable when validators evolve.
-- =====================================================================
CREATE TABLE IF NOT EXISTS platform.schema_validation_results (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    schema_version_id   UUID NOT NULL REFERENCES platform.schema_definitions(id) ON DELETE CASCADE,
    validated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_valid            BOOLEAN NOT NULL,
    errors              JSONB NOT NULL DEFAULT '[]'::jsonb,
    warnings            JSONB NOT NULL DEFAULT '[]'::jsonb,
    validator_version   TEXT NOT NULL DEFAULT 'v1.0',
    triggered_by        TEXT NOT NULL DEFAULT 'manual'  -- 'manual' | 'publish_gate' | 'nightly_sweep'
);

CREATE INDEX IF NOT EXISTS idx_schema_validation_results_version
    ON platform.schema_validation_results (schema_version_id, validated_at DESC);

CREATE INDEX IF NOT EXISTS idx_schema_validation_results_invalid
    ON platform.schema_validation_results (validated_at DESC)
    WHERE is_valid = FALSE;

COMMENT ON TABLE platform.schema_validation_results IS
    'Wave 116 — append-only audit of schema validator runs. Latest row per schema_version_id is the canonical validity state.';

-- =====================================================================
-- 3. Demo schema definitions — one per validatable kind, exercising the
--    QA catalog scenarios.
--
-- Coded with prefix WAVE116_ so existing DEFAULT rows from 0035 aren't
-- disturbed. Each row is draft so admin can flip them to published once
-- the validator passes (the new ValidatorRegistry refuses publish on
-- error per Wave 116's publish-gate contract).
-- =====================================================================

-- Onboarding demo — exercises TC-SOB-001 (.exe reject), TC-SOB-002 (size),
-- TC-SOB-012 (OCR confidence threshold), TC-SOB-014 (per-customer-type
-- resolution).
INSERT INTO platform.schema_definitions
    (id, kind, code, version_no, name, description, body, status, created_at, updated_at)
VALUES (
    uuid_generate_v4(),
    'service',  -- onboarding rides on service kind until ENUM extends
    'WAVE116_ONBOARDING_RESIDENTIAL',
    1,
    'Onboarding — Residential (Wave 116 demo)',
    'Demo onboarding schema for residential customer. Exercises Wave 116 OnboardingValidator.',
    '{
        "required_documents": [
            {"code": "ktp",   "label": "KTP Identitas",         "allowed_formats": ["jpeg","png","pdf"]},
            {"code": "kk",    "label": "Kartu Keluarga",        "allowed_formats": ["jpeg","png","pdf"]}
        ],
        "min_ocr_confidence": 0.80,
        "max_doc_size_mb": 10,
        "survey_questions": [
            {"code": "household_size",     "label": "Berapa orang di rumah?",    "type": "int"},
            {"code": "current_provider",   "label": "ISP saat ini?",             "type": "text"}
        ],
        "auto_approve_thresholds": {
            "enabled": true,
            "min_confidence": 0.92
        },
        "timeline_sla_hours": 24
    }'::jsonb,
    'draft',
    NOW(),
    NOW()
)
ON CONFLICT DO NOTHING;

-- Billing demo — exercises TC-SBE-001..006 (mid-cycle, anniversary edge).
INSERT INTO platform.schema_definitions
    (id, kind, code, version_no, name, description, body, status, created_at, updated_at)
VALUES (
    uuid_generate_v4(),
    'billing',
    'WAVE116_BILLING_MONTHLY',
    1,
    'Billing — Monthly Anniversary (Wave 116 demo)',
    'Demo billing schema with monthly anniversary cycle, exclusive tax, no proration.',
    '{
        "cycle_day": 1,
        "currency": "IDR",
        "prorate_policy": "full_period",
        "defer_policy": "first_invoice",
        "tax_mode": "exclusive",
        "tax_pct": 0.11,
        "late_fee_grace_days": 7,
        "min_charge_idr": 10000
    }'::jsonb,
    'draft',
    NOW(),
    NOW()
)
ON CONFLICT DO NOTHING;

-- Service demo — exercises TC-SSD SLA tier resolution.
INSERT INTO platform.schema_definitions
    (id, kind, code, version_no, name, description, body, status, created_at, updated_at)
VALUES (
    uuid_generate_v4(),
    'service',
    'WAVE116_SERVICE_RESIDENTIAL_50M',
    1,
    'Service — Residential 50/25 Mbps Bronze (Wave 116 demo)',
    'Residential 50/25 plan with bronze SLA. Exercises Wave 116 ServiceValidator monotonic-thresholds + speed-sanity checks.',
    '{
        "plan_name": "Home 50",
        "download_mbps": 50,
        "upload_mbps": 25,
        "sla_tier": "bronze",
        "data_cap_gb": null,
        "qos_priority": 3,
        "supports_static_ip": false,
        "bundled_services": [],
        "radius_profile_template": "RES_50_25",
        "degradation_policy": {
            "warn_threshold_pct": 80,
            "soft_throttle_pct": 95,
            "hard_disconnect_pct": 100
        }
    }'::jsonb,
    'draft',
    NOW(),
    NOW()
)
ON CONFLICT DO NOTHING;

-- Commission demo — exercises TC-SCD-006..025 (clawback, splits, ramp).
INSERT INTO platform.schema_definitions
    (id, kind, code, version_no, name, description, body, status, created_at, updated_at)
VALUES (
    uuid_generate_v4(),
    'commission',
    'WAVE116_COMMISSION_SALES_5PARTY',
    1,
    'Commission — Sales 5-party split (Wave 116 demo)',
    '5-party split with 90-day clawback, 3-month ramp, on_paid trigger. Exercises Wave 116 CommissionValidator.',
    '{
        "trigger_event": "on_paid",
        "recipient_role": "sales_person",
        "base_amount_basis": "first_invoice_pct",
        "percentage": 0.15,
        "clawback_days": 90,
        "split_rules": [
            {"role": "sales_person",          "pct": 0.50},
            {"role": "sales_manager",         "pct": 0.15},
            {"role": "sales_branch",          "pct": 0.10},
            {"role": "infrastructure_branch", "pct": 0.05},
            {"role": "company",               "pct": 0.20}
        ],
        "ramp_months": 3,
        "cap_idr": null,
        "requires_kyc": true,
        "min_subscription_months": 1
    }'::jsonb,
    'draft',
    NOW(),
    NOW()
)
ON CONFLICT DO NOTHING;

-- Suspension demo — exercises TC-SSE warn → soft → hard cadence.
INSERT INTO platform.schema_definitions
    (id, kind, code, version_no, name, description, body, status, created_at, updated_at)
VALUES (
    uuid_generate_v4(),
    'suspension',
    'WAVE116_SUSPENSION_STD',
    1,
    'Suspension — Standard cadence (Wave 116 demo)',
    'Warn @ T+3, soft @ T+7, hard @ T+14. 30-min RADIUS restore window. Exercises Wave 116 SuspensionValidator monotonicity.',
    '{
        "warn_grace_days": 3,
        "soft_suspend_grace_days": 7,
        "hard_suspend_grace_days": 14,
        "requires_supervisor_for_hard": true,
        "radius_restore_window_minutes": 30,
        "notification_channels": ["whatsapp", "email"],
        "restore_requires_full_settlement": false,
        "partial_payment_advances": true
    }'::jsonb,
    'draft',
    NOW(),
    NOW()
)
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 4. New permission: platform.schema.validate
--
-- Read of validation results piggybacks on existing platform.schema.read.
-- Validate triggers (manual, all, publish-gate) require the new
-- platform.schema.validate. Super-admin gets it via the role_permissions
-- backfill.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('platform', 'schema.validate', 'Trigger schema content validators (manual + sweep) and view validation results')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'platform'
  AND p.action = 'schema.validate'
ON CONFLICT DO NOTHING;

COMMIT;
