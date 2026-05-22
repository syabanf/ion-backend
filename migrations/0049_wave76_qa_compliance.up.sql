-- 0049 — Wave 76 QA Compliance batch
--
-- Closes:
--   TC-CRM-006 (Gagal)   Expand lead source CHECK to PRD's 14 values
--   TC-CRM-002 (Gagal)   Lead type broadband/enterprise capturable
--   TC-CRM-007 (Gagal)   Add referrer_customer_id column to crm.leads
--   TC-CRM-008 (Gagal)   (implicit) reference must be an active customer
--   TC-PRD-021 (Gagal)   Filter schemas by customer_type at assignment time
--   TC-CRM-014 (Blocked) Configurable overdue threshold via platform_config
--   TC-RAD-019 (Gagal)   NOC-only credential regeneration permission
--
-- Design choices:
--   - All column adds are nullable + default-safe so existing rows
--     continue to load without backfill.
--   - The new CHECK on leads_source preserves the legacy 5 values
--     plus PRD §6.3's additional sources.
--   - The referrer_customer_id FK uses ON DELETE SET NULL so deleting
--     an old customer doesn't cascade into deleting historical leads.
--   - Permissions follow the existing (module, action) shape from 0001
--     and the role-grant pattern used by 0047.

BEGIN;

-- ============================================================
-- TC-CRM-006 — Expand lead source enum to the full PRD set
-- ============================================================

ALTER TABLE crm.leads DROP CONSTRAINT IF EXISTS leads_source_check;
ALTER TABLE crm.leads ADD CONSTRAINT leads_source_check
    CHECK (source IN (
        -- Phase-1 broadband (legacy 5):
        'manual','self_order','sales_app','referral','cs_referral',
        -- Phase-1 expanded sources per PRD §6.3:
        'cold_call','website','whatsapp','social_media_dm',
        'voip_call','line_call','walk_in','event','partner'
    ));

-- ============================================================
-- TC-CRM-002 — Capture lead_type (broadband vs enterprise)
-- ============================================================

ALTER TABLE crm.leads
    ADD COLUMN IF NOT EXISTS lead_type TEXT NOT NULL DEFAULT 'broadband'
        CHECK (lead_type IN ('broadband','enterprise'));

CREATE INDEX IF NOT EXISTS idx_crm_leads_lead_type
    ON crm.leads (lead_type, status);

-- ============================================================
-- TC-CRM-007 / TC-CRM-008 — Referrer customer link
-- ============================================================

ALTER TABLE crm.leads
    ADD COLUMN IF NOT EXISTS referrer_customer_id UUID
        REFERENCES crm.customers(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_crm_leads_referrer
    ON crm.leads (referrer_customer_id)
    WHERE referrer_customer_id IS NOT NULL;

-- ============================================================
-- TC-PRD-021 — Schema definitions scoped by customer_type
-- ============================================================

ALTER TABLE platform.schema_definitions
    ADD COLUMN IF NOT EXISTS customer_type TEXT;
    -- NULL = applies to all customer types (back-compat default).

CREATE INDEX IF NOT EXISTS idx_schema_def_kind_customer_status
    ON platform.schema_definitions (kind, customer_type, status);

-- ============================================================
-- TC-CRM-014 — Configurable lead-overdue threshold
-- ============================================================
--
-- identity.platform_config is the KV store used elsewhere (see 0001).
-- Admins can change this via /api/identity/platform-config; the
-- handler at phase2.go:215 will read it when no `days` query param
-- is supplied (still overridable per-call for ad-hoc reporting).

INSERT INTO identity.platform_config (config_key, config_value)
VALUES ('lead_overdue_days', '7')
ON CONFLICT (config_key) DO NOTHING;

-- ============================================================
-- TC-RAD-019 — NOC-only RADIUS credential regeneration
-- ============================================================
--
-- Adds the new permission and grants it to NOC + Super Admin only.
-- The handler (Wave 76) checks this via RequirePermission.

INSERT INTO identity.permissions (module, action, description) VALUES
    ('network', 'radius.regenerate',
     'Regenerate ION Radius credentials for an active customer. NOC-only — technicians cannot rotate creds even mid-WO.')
ON CONFLICT (module, action) DO NOTHING;

-- Grant to NOC.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc'
  AND p.module = 'network' AND p.action = 'radius.regenerate'
ON CONFLICT DO NOTHING;

-- Grant to Super Admin (defensive — super_admin should already have
-- everything via the CROSS JOIN seed, but new perms post-seed need
-- explicit reconciliation).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'network' AND p.action = 'radius.regenerate'
ON CONFLICT DO NOTHING;

COMMIT;
