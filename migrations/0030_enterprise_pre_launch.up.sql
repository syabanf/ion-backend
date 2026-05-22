-- 0030_enterprise_pre_launch.up.sql
--
-- Pre-launch enterprise gap closure. Bundles 12 PRD gaps (E1–E12).
-- One migration so the schema lands atomically; the code changes
-- ride on subsequent commits.
--
-- Covered here:
--   E2  approval chain reset reason
--   E4  vendor SLA due_at on BOQ lines + reminder log
--   E5  termin/invoice plan (split invoice into N scheduled items)
--   E6  tax_pct + tax_amount on BOQs and invoices
--   E7  PO document storage
--   E8  payment proof attachment
--   E9  EWO checklist items + progress
--   E10 notifications log (any-actor inbox)
--   E11 enterprise_projects + project_sites + enterprise_services
--   E12 RFQ records (Warm→Hot Sales Support handoff)
--
-- E1 (save-time price-floor validation) and E3 (approver reassign)
-- are pure code changes — no schema required.

BEGIN;

-- =====================================================================
-- E2 — approval chain reset reason
-- =====================================================================
-- When a Sales rep edits a BOQ line mid-approval, the chain has to
-- reset so prior approvals can't carry over into a different commercial
-- state. We capture the trigger reason on the (existing) reset event
-- so the audit trail explains why.

ALTER TABLE enterprise.approval_instances
    ADD COLUMN IF NOT EXISTS reset_reason TEXT NOT NULL DEFAULT '';
COMMENT ON COLUMN enterprise.approval_instances.reset_reason IS
    'When status flips to superseded_reset, captures the trigger: pricing_changed, line_added, line_removed, etc.';

-- =====================================================================
-- E4 — vendor SLA due_at + reminder log
-- =====================================================================
-- BOQ lines assigned to a vendor have an implicit SLA window from
-- assignment. due_at is computed by the usecase at assignment time
-- (default 48h). The reminder log dedupes reminders so we don't spam
-- the vendor on every tick.

ALTER TABLE enterprise.boq_lines
    ADD COLUMN IF NOT EXISTS vendor_due_at TIMESTAMPTZ;
COMMENT ON COLUMN enterprise.boq_lines.vendor_due_at IS
    'Soft SLA for vendor to fill in vendor_unit_cost. Set on assignment; nulled when filled.';

CREATE TABLE IF NOT EXISTS enterprise.boq_line_reminders (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    boq_line_id     UUID NOT NULL
        REFERENCES enterprise.boq_lines(id) ON DELETE CASCADE,
    -- Bucket the reminder so the same line doesn't get pinged twice for
    -- the same threshold. T-24h fires once when 24h <= remaining < 48h;
    -- T-8h fires once when 0 < remaining < 24h; overdue fires once when
    -- remaining <= 0.
    bucket          TEXT NOT NULL
        CHECK (bucket IN ('t_minus_24h', 't_minus_8h', 'overdue')),
    fired_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (boq_line_id, bucket)
);
CREATE INDEX IF NOT EXISTS idx_boq_line_reminders_fired
    ON enterprise.boq_line_reminders(fired_at DESC);

-- =====================================================================
-- E5 — termin/invoice plan
-- =====================================================================
-- Finance breaks the BOQ total into N termin (instalments) BEFORE
-- issuing invoices. Each plan item becomes an invoice when it gets
-- "issued"; the plan tracks coverage vs the source quotation total
-- (Edge #9 tolerance check lives here).

CREATE TABLE IF NOT EXISTS enterprise.invoice_plans (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quotation_id    UUID NOT NULL,
    opportunity_id  UUID NOT NULL,
    boq_version_id  UUID NOT NULL,
    plan_number     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'active', 'cancelled')),
    total_amount    NUMERIC(18,2) NOT NULL,
    planned_amount  NUMERIC(18,2) NOT NULL DEFAULT 0,
    currency        TEXT NOT NULL DEFAULT 'IDR',
    tolerance_pct   NUMERIC(6,3) NOT NULL DEFAULT 0.5,
    notes           TEXT NOT NULL DEFAULT '',
    created_by      UUID,
    revision        INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_plans_quotation
    ON enterprise.invoice_plans(quotation_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_plans_plan_number
    ON enterprise.invoice_plans(plan_number);
CREATE INDEX IF NOT EXISTS idx_invoice_plans_status
    ON enterprise.invoice_plans(status);
CREATE TRIGGER trg_invoice_plans_touch
    BEFORE UPDATE ON enterprise.invoice_plans
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TABLE IF NOT EXISTS enterprise.invoice_plan_items (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    plan_id         UUID NOT NULL
        REFERENCES enterprise.invoice_plans(id) ON DELETE CASCADE,
    seq_no          INT NOT NULL CHECK (seq_no >= 1),
    label           TEXT NOT NULL,
    amount          NUMERIC(18,2) NOT NULL CHECK (amount > 0),
    due_offset_days INT NOT NULL DEFAULT 30,
    -- Once an invoice is issued from a termin item we record the
    -- linkage so the same item can't be billed twice.
    invoice_id      UUID REFERENCES enterprise.invoices(id) ON DELETE SET NULL,
    issued_at       TIMESTAMPTZ,
    notes           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (plan_id, seq_no)
);
CREATE INDEX IF NOT EXISTS idx_invoice_plan_items_plan
    ON enterprise.invoice_plan_items(plan_id, seq_no);

-- Link an issued invoice back to the plan + termin item it came from.
-- Optional — direct (non-termin) invoices still work.
ALTER TABLE enterprise.invoices
    ADD COLUMN IF NOT EXISTS invoice_plan_id UUID
        REFERENCES enterprise.invoice_plans(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS invoice_plan_item_id UUID
        REFERENCES enterprise.invoice_plan_items(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_invoices_plan
    ON enterprise.invoices(invoice_plan_id);

-- =====================================================================
-- E6 — tax / VAT 11% on BOQ + invoice
-- =====================================================================
-- Tax is computed from a subtotal; the existing total_amount column on
-- invoices stays as the GRAND total (subtotal + tax) for backwards
-- compat. We add subtotal + tax_pct + tax_amount so the FE can render
-- the breakdown. Same shape on BOQ for symmetry.

ALTER TABLE enterprise.boq_versions
    ADD COLUMN IF NOT EXISTS subtotal_amount NUMERIC(18,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tax_pct NUMERIC(6,3) NOT NULL DEFAULT 11.0,
    ADD COLUMN IF NOT EXISTS tax_amount NUMERIC(18,2) NOT NULL DEFAULT 0;
COMMENT ON COLUMN enterprise.boq_versions.tax_pct IS 'PPN 11% by default (Indonesia). Per-deal override allowed.';

ALTER TABLE enterprise.invoices
    ADD COLUMN IF NOT EXISTS subtotal_amount NUMERIC(18,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tax_pct NUMERIC(6,3) NOT NULL DEFAULT 11.0,
    ADD COLUMN IF NOT EXISTS tax_amount NUMERIC(18,2) NOT NULL DEFAULT 0;

-- Same on plan + items.
ALTER TABLE enterprise.invoice_plans
    ADD COLUMN IF NOT EXISTS subtotal_amount NUMERIC(18,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tax_pct NUMERIC(6,3) NOT NULL DEFAULT 11.0,
    ADD COLUMN IF NOT EXISTS tax_amount NUMERIC(18,2) NOT NULL DEFAULT 0;

-- =====================================================================
-- E7 — PO document storage
-- =====================================================================
-- Customer-issued Purchase Order. Stored as a file reference (we keep
-- only metadata + URL — actual blob lives wherever uploads are
-- proxied through the gateway). One PO per opportunity at MVP; the
-- model supports multiple revisions via po_revision.

CREATE TABLE IF NOT EXISTS enterprise.po_documents (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    opportunity_id  UUID NOT NULL,
    po_number       TEXT NOT NULL,
    po_revision     INT NOT NULL DEFAULT 1,
    file_url        TEXT NOT NULL,
    file_name       TEXT NOT NULL,
    file_size_bytes BIGINT NOT NULL DEFAULT 0,
    content_type    TEXT NOT NULL DEFAULT 'application/pdf',
    issued_by_pic   TEXT NOT NULL DEFAULT '',
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_by     UUID,
    notes           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (opportunity_id, po_revision)
);
CREATE INDEX IF NOT EXISTS idx_po_documents_opp
    ON enterprise.po_documents(opportunity_id, received_at DESC);

-- =====================================================================
-- E8 — payment proof attachment
-- =====================================================================
-- Customer-provided proof of payment (bank transfer slip, check image,
-- etc). Linked to an invoice_payment row. Same blob-by-URL pattern.

CREATE TABLE IF NOT EXISTS enterprise.payment_proofs (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    invoice_payment_id  UUID NOT NULL
        REFERENCES enterprise.invoice_payments(id) ON DELETE CASCADE,
    file_url            TEXT NOT NULL,
    file_name           TEXT NOT NULL,
    file_size_bytes     BIGINT NOT NULL DEFAULT 0,
    content_type        TEXT NOT NULL DEFAULT 'application/pdf',
    uploaded_by         UUID,
    notes               TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_payment_proofs_payment
    ON enterprise.payment_proofs(invoice_payment_id);

-- =====================================================================
-- E9 — EWO checklist items + progress %
-- =====================================================================
-- EWOs need a delivery checklist so the field team can mark progress
-- as they work. The progress % is computed as
--   completed_items / total_items.

CREATE TABLE IF NOT EXISTS enterprise.ewo_checklist_items (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ewo_id          UUID NOT NULL
        REFERENCES enterprise.ewos(id) ON DELETE CASCADE,
    seq_no          INT NOT NULL CHECK (seq_no >= 1),
    label           TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'completed', 'skipped')),
    completed_at    TIMESTAMPTZ,
    completed_by    UUID,
    notes           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (ewo_id, seq_no)
);
CREATE INDEX IF NOT EXISTS idx_ewo_checklist_ewo
    ON enterprise.ewo_checklist_items(ewo_id, seq_no);

CREATE TRIGGER trg_ewo_checklist_touch
    BEFORE UPDATE ON enterprise.ewo_checklist_items
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- Field WO linkage (E9b). The actual field.work_orders table lives in
-- the field-svc schema; we store the id as a plain UUID + nullable for
-- soft coupling.
ALTER TABLE enterprise.ewos
    ADD COLUMN IF NOT EXISTS field_work_order_id UUID,
    ADD COLUMN IF NOT EXISTS progress_pct NUMERIC(6,3) NOT NULL DEFAULT 0;

-- =====================================================================
-- E10 — notifications log
-- =====================================================================
-- Generic in-app notification record. Producer modules INSERT; the
-- recipient sees them in a bell dropdown until marked read. We
-- deliberately keep it module-agnostic (`subject_type` + `subject_id`)
-- so adding a new event type doesn't require a schema change.

CREATE TABLE IF NOT EXISTS enterprise.notifications (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    recipient_user_id UUID NOT NULL,
    kind            TEXT NOT NULL,
    -- Examples: boq.submit, boq.approval_pending, boq.approved, boq.rejected,
    --           negotiation.round_pending, negotiation.round_approved,
    --           negotiation.round_rejected, ewo.assigned, invoice.issued,
    --           invoice.paid, boq_line.vendor_due_soon
    subject_type    TEXT NOT NULL,
    subject_id      UUID NOT NULL,
    title           TEXT NOT NULL,
    body            TEXT NOT NULL DEFAULT '',
    severity        TEXT NOT NULL DEFAULT 'info'
        CHECK (severity IN ('info', 'warn', 'critical')),
    read_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_notifications_recipient
    ON enterprise.notifications(recipient_user_id, read_at, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_subject
    ON enterprise.notifications(subject_type, subject_id);

-- =====================================================================
-- E11 — enterprise projects, sites, services (multi-site delivery)
-- =====================================================================
-- After a quotation is accepted + EWO created, fulfillment splits the
-- scope into project sites (one per physical location) and per-site
-- services (the actual deliverables — connectivity, equipment, etc).
-- This is the bridge from the Sales-side "deal" model to the Field-side
-- "execution" model.

CREATE TABLE IF NOT EXISTS enterprise.projects (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_number  TEXT NOT NULL,
    quotation_id    UUID NOT NULL,
    opportunity_id  UUID NOT NULL,
    boq_version_id  UUID NOT NULL,
    status          TEXT NOT NULL DEFAULT 'planning'
        CHECK (status IN ('planning', 'in_progress', 'completed', 'cancelled')),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ,
    cancel_reason   TEXT NOT NULL DEFAULT '',
    project_manager_user_id UUID,
    notes           TEXT NOT NULL DEFAULT '',
    revision        INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_quotation
    ON enterprise.projects(quotation_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_number
    ON enterprise.projects(project_number);
CREATE INDEX IF NOT EXISTS idx_projects_status
    ON enterprise.projects(status);
CREATE TRIGGER trg_projects_touch
    BEFORE UPDATE ON enterprise.projects
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TABLE IF NOT EXISTS enterprise.project_sites (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_id      UUID NOT NULL
        REFERENCES enterprise.projects(id) ON DELETE CASCADE,
    site_code       TEXT NOT NULL,
    site_name       TEXT NOT NULL,
    address         TEXT NOT NULL DEFAULT '',
    lat             NUMERIC(10,6),
    lng             NUMERIC(10,6),
    pic_name        TEXT NOT NULL DEFAULT '',
    pic_phone       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'active', 'cancelled')),
    activated_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, site_code)
);
CREATE INDEX IF NOT EXISTS idx_project_sites_project
    ON enterprise.project_sites(project_id);
CREATE TRIGGER trg_project_sites_touch
    BEFORE UPDATE ON enterprise.project_sites
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

CREATE TABLE IF NOT EXISTS enterprise.enterprise_services (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    project_site_id UUID NOT NULL
        REFERENCES enterprise.project_sites(id) ON DELETE CASCADE,
    boq_line_id     UUID,  -- soft link to the originating BOQ line
    service_code    TEXT NOT NULL,
    service_name    TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'provisioning', 'active', 'suspended', 'terminated')),
    activated_at    TIMESTAMPTZ,
    terminated_at   TIMESTAMPTZ,
    notes           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_enterprise_services_site
    ON enterprise.enterprise_services(project_site_id);
CREATE INDEX IF NOT EXISTS idx_enterprise_services_status
    ON enterprise.enterprise_services(status);
CREATE TRIGGER trg_enterprise_services_touch
    BEFORE UPDATE ON enterprise.enterprise_services
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- E12 — RFQ (Request For Quotation) records
-- =====================================================================
-- When an opportunity moves Warm → Hot, Sales raises an RFQ that hands
-- off to Sales Support to build the BOQ. This captures the "ask" so the
-- handoff has structure (requirements, constraints, deadline).

CREATE TABLE IF NOT EXISTS enterprise.rfqs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    rfq_number      TEXT NOT NULL,
    opportunity_id  UUID NOT NULL,
    status          TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'in_progress', 'fulfilled', 'cancelled')),
    requested_by    UUID,
    assigned_to     UUID,
    requirements    TEXT NOT NULL DEFAULT '',
    constraints     TEXT NOT NULL DEFAULT '',
    deadline_at     TIMESTAMPTZ,
    fulfilled_at    TIMESTAMPTZ,
    fulfilled_boq_id UUID,  -- soft link to the BOQ that satisfies it
    cancelled_at    TIMESTAMPTZ,
    cancel_reason   TEXT NOT NULL DEFAULT '',
    notes           TEXT NOT NULL DEFAULT '',
    revision        INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_rfqs_number
    ON enterprise.rfqs(rfq_number);
CREATE INDEX IF NOT EXISTS idx_rfqs_opp
    ON enterprise.rfqs(opportunity_id);
CREATE INDEX IF NOT EXISTS idx_rfqs_status
    ON enterprise.rfqs(status);
CREATE INDEX IF NOT EXISTS idx_rfqs_assigned
    ON enterprise.rfqs(assigned_to);
CREATE TRIGGER trg_rfqs_touch
    BEFORE UPDATE ON enterprise.rfqs
    FOR EACH ROW EXECUTE FUNCTION enterprise.touch_updated_at();

-- =====================================================================
-- RBAC permissions for the new surfaces
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'invoice_plan.read',    'View invoice plans + termin schedules'),
    ('enterprise', 'invoice_plan.manage',  'Create / edit / activate invoice plans'),
    ('enterprise', 'po_document.read',     'View customer PO documents'),
    ('enterprise', 'po_document.manage',   'Upload / replace customer PO documents'),
    ('enterprise', 'payment_proof.read',   'View payment proof attachments'),
    ('enterprise', 'payment_proof.manage', 'Upload payment proof attachments'),
    ('enterprise', 'ewo_checklist.manage', 'Update EWO checklist item status'),
    ('enterprise', 'notification.read',    'View own notifications'),
    ('enterprise', 'project.read',         'View enterprise projects + sites + services'),
    ('enterprise', 'project.manage',       'Create / edit projects, sites, services'),
    ('enterprise', 'rfq.read',             'View RFQ records'),
    ('enterprise', 'rfq.manage',           'Create / assign / fulfill / cancel RFQs'),
    ('enterprise', 'approval.reassign',    'Reassign a pending approval step to a different user')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin: blanket.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN (
      'invoice_plan.read', 'invoice_plan.manage',
      'po_document.read', 'po_document.manage',
      'payment_proof.read', 'payment_proof.manage',
      'ewo_checklist.manage', 'notification.read',
      'project.read', 'project.manage',
      'rfq.read', 'rfq.manage',
      'approval.reassign'
  )
ON CONFLICT DO NOTHING;

-- sales_manager: project/RFQ/EWO management; PO read; can reassign.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND p.module = 'enterprise'
  AND p.action IN (
      'po_document.read', 'po_document.manage',
      'ewo_checklist.manage', 'notification.read',
      'project.read', 'project.manage',
      'rfq.read', 'rfq.manage',
      'approval.reassign'
  )
ON CONFLICT DO NOTHING;

-- sales_rep: notification + read-only on RFQ + project (their deals).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'enterprise'
  AND p.action IN (
      'notification.read', 'rfq.read', 'project.read', 'po_document.read'
  )
ON CONFLICT DO NOTHING;

-- finance: invoice_plan + payment_proof manage; notifications.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance'
  AND p.module = 'enterprise'
  AND p.action IN (
      'invoice_plan.read', 'invoice_plan.manage',
      'payment_proof.read', 'payment_proof.manage',
      'notification.read'
  )
ON CONFLICT DO NOTHING;

COMMIT;
