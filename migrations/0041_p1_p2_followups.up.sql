-- 0041 — P1 + P2 backlog items
--
-- Adds the schema surface for: customer notifications, lead event
-- timeline, SLA-per-service binding, vendor onboarding docs, vendor
-- scorecards, ticket attachments, speedtest checklist item, and a
-- few small column adds.

BEGIN;

-- ============================================================
-- Extend lead source CHECK to include 'cs_referral'
-- (CS agent → sales lead flow)
-- ============================================================

ALTER TABLE crm.leads DROP CONSTRAINT IF EXISTS leads_source_check;
ALTER TABLE crm.leads ADD CONSTRAINT leads_source_check
    CHECK (source IN ('manual','self_order','sales_app','referral','cs_referral'));

-- ============================================================
-- Customer notifications inbox (mobile customer app)
-- ============================================================

CREATE TABLE IF NOT EXISTS crm.customer_notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES crm.customers(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,        -- ticket_reply | bast_scheduled | payment_succeeded | sla_breach | plan_change | …
    title       TEXT NOT NULL,
    body        TEXT NOT NULL,
    deep_link   TEXT,                 -- /tickets/{id}, /bills, etc.
    data        JSONB NOT NULL DEFAULT '{}'::jsonb,
    read_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cust_notif_customer
    ON crm.customer_notifications (customer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_cust_notif_unread
    ON crm.customer_notifications (customer_id, created_at DESC)
    WHERE read_at IS NULL;

-- ============================================================
-- Lead events — sales-side interaction history timeline
-- ============================================================

CREATE TABLE IF NOT EXISTS crm.lead_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lead_id     UUID NOT NULL REFERENCES crm.leads(id) ON DELETE CASCADE,
    actor_user_id UUID,            -- nullable: system events
    kind        TEXT NOT NULL,     -- created | status_change | doc_uploaded | note | coverage_checked | converted | …
    summary     TEXT NOT NULL,
    data        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_lead_events_lead
    ON crm.lead_events (lead_id, created_at DESC);

-- Seed the timeline for existing leads so the UI doesn't render an
-- empty timeline for old rows. A single "created" event is enough.
INSERT INTO crm.lead_events (lead_id, kind, summary, created_at)
SELECT id, 'created',
       'Lead created (backfill)',
       created_at
FROM crm.leads
WHERE NOT EXISTS (
    SELECT 1 FROM crm.lead_events e WHERE e.lead_id = crm.leads.id
);

-- ============================================================
-- SLA template binding to a service catalog row
-- ============================================================

ALTER TABLE enterprise.service_catalog
    ADD COLUMN IF NOT EXISTS default_sla_template_id UUID
        REFERENCES enterprise.sla_templates(id) ON DELETE SET NULL;

-- ============================================================
-- Vendor onboarding documents
-- ============================================================

CREATE TABLE IF NOT EXISTS enterprise.vendor_documents (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_id   UUID NOT NULL,         -- soft-FK to enterprise.vendors (any vendor table variant)
    kind        TEXT NOT NULL CHECK (kind IN ('nib','npwp','akta','sk_pendirian','siup','other')),
    file_url    TEXT NOT NULL,
    file_name   TEXT NOT NULL,
    bytes       INT NOT NULL DEFAULT 0,
    uploaded_by UUID,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    verified_at TIMESTAMPTZ,
    verified_by UUID,
    notes       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_vendor_docs_vendor
    ON enterprise.vendor_documents (vendor_id, uploaded_at DESC);

-- ============================================================
-- Vendor performance metrics — append-only fact rows
-- ============================================================

CREATE TABLE IF NOT EXISTS enterprise.vendor_metrics (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_id   UUID NOT NULL,
    period_month DATE NOT NULL,       -- first of month
    orders_total INT NOT NULL DEFAULT 0,
    orders_on_time INT NOT NULL DEFAULT 0,
    defects_reported INT NOT NULL DEFAULT 0,
    avg_response_hours NUMERIC(10,2),
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (vendor_id, period_month)
);

-- ============================================================
-- Cross-area dispatch — add WO columns
-- ============================================================

ALTER TABLE field.work_orders
    ADD COLUMN IF NOT EXISTS cross_area_target_branch_id UUID,
    ADD COLUMN IF NOT EXISTS cross_area_reason TEXT,
    ADD COLUMN IF NOT EXISTS cross_area_requested_at TIMESTAMPTZ;

-- ============================================================
-- Mid-schedule incoming WO — priority insertion log
-- ============================================================

CREATE TABLE IF NOT EXISTS field.priority_insertions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wo_id        UUID NOT NULL,
    inserted_by  UUID NOT NULL,
    tech_user_id UUID NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    accepted     BOOLEAN,
    accepted_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_priority_ins_tech
    ON field.priority_insertions (tech_user_id, created_at DESC);

-- ============================================================
-- Ticket attachments — array of object urls per message
-- ============================================================

ALTER TABLE field.ticket_messages
    ADD COLUMN IF NOT EXISTS attachments JSONB NOT NULL DEFAULT '[]'::jsonb;

-- ============================================================
-- Checklist speedtest item type — extend the existing CHECK
-- ============================================================

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.check_constraints
    WHERE constraint_name = 'checklist_template_items_item_type_check'
  ) THEN
    ALTER TABLE field.checklist_template_items
        DROP CONSTRAINT checklist_template_items_item_type_check;
    ALTER TABLE field.checklist_template_items
        ADD CONSTRAINT checklist_template_items_item_type_check
        CHECK (item_type IN ('photo','text','number','checkbox','qr_scan',
                              'signature','gps_location','optical_power',
                              'speedtest'));
  END IF;
END$$;

-- ============================================================
-- Coverage-checked event auto-write trigger? — no, keep that to
-- the application layer; we already log via lead_events.
-- ============================================================

-- ============================================================
-- Permissions
-- ============================================================

INSERT INTO identity.permissions (module, action, description) VALUES
    ('crm','notification.read','Read own customer notifications'),
    ('crm','lead_event.read','Read lead interaction timeline'),
    ('field','cross_area.request','Request a tech from another area'),
    ('field','priority_insert','Insert a high-priority WO mid-schedule'),
    ('enterprise','vendor_doc.manage','Upload + verify vendor onboarding docs'),
    ('enterprise','vendor_metric.read','Read vendor scorecard'),
    ('warehouse','stock_dashboard.read','Read cross-warehouse stock dashboard')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND (p.module || '.' || p.action) IN (
    'crm.notification.read','crm.lead_event.read',
    'field.cross_area.request','field.priority_insert',
    'enterprise.vendor_doc.manage','enterprise.vendor_metric.read',
    'warehouse.stock_dashboard.read'
  )
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('sales_rep','sales_manager','operations_admin','cs_agent','cs_supervisor')
  AND (p.module || '.' || p.action) = 'crm.lead_event.read'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('team_leader','operations_admin')
  AND (p.module || '.' || p.action) IN ('field.cross_area.request','field.priority_insert')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('warehouse_manager','warehouse_staff','operations_admin')
  AND (p.module || '.' || p.action) = 'warehouse.stock_dashboard.read'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name IN ('operations_admin','finance_admin')
  AND (p.module || '.' || p.action) IN ('enterprise.vendor_doc.manage','enterprise.vendor_metric.read')
ON CONFLICT DO NOTHING;

COMMIT;
