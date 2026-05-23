-- Wave 107 — Provider & Vendor Input bounded context.
--
-- Closes the gap surfaced by the Wave 91 audit: a first-class provider
-- registry with capabilities + submission state machine + daily metrics
-- so the Phase 1 "Provider & Vendor Input" TCs (15 cases) have a
-- domain to land on. The existing BOQ `vendor_unit_cost` /
-- `assigned_provider_company_id` columns stay untouched — this schema
-- is the *registry* + *submission inbox* that feeds those columns; the
-- BOQ math + procurement audit row remain the source of truth at
-- approval time.
--
-- Schema created: `vendor`.
-- Tables created:
--   - vendor.providers
--   - vendor.provider_capabilities
--   - vendor.provider_input_submissions
--   - vendor.provider_metrics_daily
--
-- Permissions added (module='vendor'):
--   - provider.read / provider.write
--   - submission.read / submission.write / submission.review
--   - metrics.read
--
-- Role grants:
--   - super_admin → every permission
--   - vendor_admin (created here) → provider.* + submission.*
--   - sales_manager + sales_rep → submission.read + provider.read

BEGIN;

CREATE SCHEMA IF NOT EXISTS vendor;

-- =====================================================================
-- vendor.providers — registry of internal + external vendors.
--
-- KYC + state machine: pending → active (requires KYC) → suspended
-- (with reason) → active again, OR → blacklisted (terminal). Rating /
-- jobs / revenue are denormalised aggregates updated by the metrics
-- deriver cron + by the IC-PO-accept hook in the enterprise context.
-- =====================================================================
CREATE TABLE vendor.providers (
    id                     UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name                   TEXT NOT NULL,
    npwp                   TEXT,
    contact_email          TEXT,
    contact_phone          TEXT,
    status                 TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'suspended', 'blacklisted')),
    kyc_completed          BOOLEAN NOT NULL DEFAULT FALSE,
    capabilities           JSONB NOT NULL DEFAULT '[]'::jsonb,
    rating_score           NUMERIC(3,2) NOT NULL DEFAULT 0.00,
    total_completed_jobs   INT NOT NULL DEFAULT 0,
    total_revenue          NUMERIC(18,2) NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    suspended_at           TIMESTAMPTZ,
    suspended_reason       TEXT
);

CREATE INDEX idx_vendor_providers_status
    ON vendor.providers (status);

-- Ranking index for the "top rated" picker. DESC on both columns so
-- ORDER BY rating_score DESC, total_completed_jobs DESC is index-scan.
CREATE INDEX idx_vendor_providers_ranking
    ON vendor.providers (rating_score DESC, total_completed_jobs DESC);

-- =====================================================================
-- vendor.provider_capabilities — normalized capability tags.
--
-- The providers.capabilities JSONB is a convenience snapshot; this table
-- is the authoritative join for "which providers can do X". The unique
-- key keeps a single (provider, capability) pair to one row.
-- =====================================================================
CREATE TABLE vendor.provider_capabilities (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id         UUID NOT NULL
        REFERENCES vendor.providers(id) ON DELETE CASCADE,
    capability_key      TEXT NOT NULL,
    capability_name     TEXT,
    max_capacity        INT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_vendor_provider_capabilities_unique
    ON vendor.provider_capabilities (provider_id, capability_key);

CREATE INDEX idx_vendor_provider_capabilities_key
    ON vendor.provider_capabilities (capability_key);

-- =====================================================================
-- vendor.provider_input_submissions — vendor cost-input inbox.
--
-- One row per (opportunity, provider, boq_line) submission attempt.
-- Reviewer flips status to accepted / rejected; submitter may withdraw
-- while still 'submitted'. The accepted row drives the eventual
-- BOQ.SetVendorCost in the enterprise context.
-- =====================================================================
CREATE TABLE vendor.provider_input_submissions (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    opportunity_id      UUID NOT NULL,
    provider_id         UUID NOT NULL
        REFERENCES vendor.providers(id),
    boq_line_id         UUID,
    unit_cost           NUMERIC(18,2),
    notes               TEXT,
    status              TEXT NOT NULL DEFAULT 'submitted'
        CHECK (status IN ('submitted', 'accepted', 'rejected', 'withdrawn')),
    submitted_by        UUID,
    submitted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_by         UUID,
    reviewed_at         TIMESTAMPTZ,
    rejection_reason    TEXT
);

CREATE INDEX idx_vendor_submissions_opportunity
    ON vendor.provider_input_submissions (opportunity_id, status);

CREATE INDEX idx_vendor_submissions_provider
    ON vendor.provider_input_submissions (provider_id, status);

-- =====================================================================
-- vendor.provider_metrics_daily — derived daily KPI rows.
--
-- One row per (provider, day). Populated by the MetricsDeriverDaily
-- cron in the vendor service; reads come from the AverageScoreForProvider
-- + TopRatedProviders usecase methods.
-- =====================================================================
CREATE TABLE vendor.provider_metrics_daily (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    provider_id              UUID NOT NULL
        REFERENCES vendor.providers(id) ON DELETE CASCADE,
    metric_date              DATE NOT NULL,
    jobs_completed           INT NOT NULL DEFAULT 0,
    on_time_completion_pct   NUMERIC(5,2),
    avg_response_hours       NUMERIC(8,2),
    tickets_resolved         INT NOT NULL DEFAULT 0,
    customer_satisfaction    NUMERIC(3,2),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_vendor_metrics_daily_unique
    ON vendor.provider_metrics_daily (provider_id, metric_date);

CREATE INDEX idx_vendor_metrics_daily_date
    ON vendor.provider_metrics_daily (metric_date DESC);

-- =====================================================================
-- Demo seed — two providers with capabilities so a fresh dev DB has
-- something for the picker. Idempotent via ON CONFLICT DO NOTHING.
-- =====================================================================
INSERT INTO vendor.providers
    (id, name, npwp, contact_email, status, kyc_completed,
     capabilities, rating_score, total_completed_jobs)
VALUES
    ('00000000-0000-0000-0000-000000000001'::uuid,
     'PT Demo Fiber Contractor',
     '01.234.567.8-901.000',
     'ops@demofiber.id',
     'active', TRUE,
     '["fiber_drop", "splicing"]'::jsonb,
     4.50, 120),
    ('00000000-0000-0000-0000-000000000002'::uuid,
     'CV Demo Tower Services',
     '02.345.678.9-012.000',
     'ops@demotower.id',
     'active', TRUE,
     '["tower_climb", "antenna_install"]'::jsonb,
     4.20, 45)
ON CONFLICT (id) DO NOTHING;

INSERT INTO vendor.provider_capabilities
    (provider_id, capability_key, capability_name, max_capacity)
VALUES
    ('00000000-0000-0000-0000-000000000001'::uuid, 'fiber_drop',      'Fiber Drop Cable Install', 50),
    ('00000000-0000-0000-0000-000000000001'::uuid, 'splicing',        'OTDR + Fusion Splicing', 30),
    ('00000000-0000-0000-0000-000000000002'::uuid, 'tower_climb',     'Tower Climb + Rigging', 10),
    ('00000000-0000-0000-0000-000000000002'::uuid, 'antenna_install', 'Antenna Installation', 8)
ON CONFLICT (provider_id, capability_key) DO NOTHING;

-- =====================================================================
-- Permissions + role grants.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('vendor', 'provider.read',     'View vendor/provider registry rows'),
    ('vendor', 'provider.write',    'Create / update / state-transition providers'),
    ('vendor', 'submission.read',   'View vendor input submissions'),
    ('vendor', 'submission.write',  'Create / withdraw vendor input submissions'),
    ('vendor', 'submission.review', 'Accept / reject vendor input submissions'),
    ('vendor', 'metrics.read',      'View provider performance metrics')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin gets every vendor permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'vendor'
ON CONFLICT DO NOTHING;

-- Create the vendor_admin role if missing (idempotent).
INSERT INTO identity.roles (name, description) VALUES
    ('vendor_admin', 'Vendor administrator — manages provider registry + reviews vendor cost submissions')
ON CONFLICT (name) DO NOTHING;

-- vendor_admin gets provider.* + submission.* (read/write/review).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'vendor_admin'
  AND p.module = 'vendor'
  AND p.action IN (
      'provider.read', 'provider.write',
      'submission.read', 'submission.write', 'submission.review',
      'metrics.read'
  )
ON CONFLICT DO NOTHING;

-- sales_manager + sales_rep get submission.read + provider.read so they
-- can see whose cost feeds their BOQ without being able to touch the
-- review queue.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('sales_manager', 'sales_rep')
  AND p.module = 'vendor'
  AND p.action IN ('provider.read', 'submission.read')
ON CONFLICT DO NOTHING;

COMMIT;
