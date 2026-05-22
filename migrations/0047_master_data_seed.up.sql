-- 0047 — Master data seed (production-ready first-boot).
--
-- Goal: a freshly-migrated database is *operable on first login*. Out of the
-- box the prior migrations leave the platform half-empty:
--
--   * identity.roles — yes, 17 roles seeded in 0001 (super_admin, sales_rep, ...)
--   * identity.permissions — NO catalog; the dashboard checks `Can permission=`
--     against `module.action` keys that don't exist yet
--   * identity.role_permissions — NO mapping; nobody can do anything
--   * identity.branches — empty; users can't even self-assign
--   * crm.products — empty; can't create a lead
--   * enterprise.approval_templates — empty; BOQs can't be submitted
--   * enterprise.ewo_checklist_templates — empty; install WO has no checklist
--
-- This migration closes those gaps. Every insert is idempotent
-- (ON CONFLICT DO NOTHING on the natural keys) so the migration is safe to
-- re-run, and the down migration reverses *only* the rows this file inserts.
--
-- Already-seeded master data (do NOT touch):
--   * identity.roles                  — 0001_platform_core
--   * identity.platform_config        — 0003_admin_defaults
--   * billing.policies (id=1)         — 0014_billing_r2
--   * enterprise.sla_templates        — 0026_enterprise_phase3
--   * platform.schema_definitions     — 0035_seed_default_schemas
--
-- After this migration, the bootstrap order on a fresh deploy is:
--   1. Run all migrations (this one populates the catalog).
--   2. Operator creates the first super_admin user (out-of-band script).
--   3. That super_admin signs in and finishes the long-tail setup
--      (additional branches, technician profiles, real BOQ approval chains).

BEGIN;

-- =====================================================================
-- 1. identity.permissions — canonical (module, action) catalog
--
-- Sourced by grepping every `Can permission=` and `RequireAuth permission=`
-- in the dashboard. Schema is (module TEXT, action TEXT) UNIQUE so the key
-- `crm.lead.read` splits as module='crm', action='lead.read'.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    -- admin
    ('admin',      'platform_config.read',          'View platform configuration KV'),
    ('admin',      'platform_config.manage',        'Edit platform configuration KV'),

    -- billing
    ('billing',    'commission.read',               'View commission ledger'),
    ('billing',    'cycles.read',                   'View billing cycle runs'),
    ('billing',    'cycles.run',                    'Trigger a billing cycle run'),
    ('billing',    'invoice.create',                'Create a billing invoice'),
    ('billing',    'invoice.read',                  'View billing invoices'),
    ('billing',    'invoice.void',                  'Void a billing invoice'),
    ('billing',    'payment.record',                'Record a customer payment'),
    ('billing',    'policy.manage',                 'Edit billing policy (grace, late fee, suspension)'),
    ('billing',    'policy.read',                   'View billing policy'),
    ('billing',    'referral.read',                 'View referral payouts'),
    ('billing',    'termination.manage',            'Process termination requests'),
    ('billing',    'termination.read',              'View termination requests'),

    -- crm
    ('crm',        'customer.read',                 'View customer records'),
    ('crm',        'dashboard.read',                'View CRM dashboard'),
    ('crm',        'lead.convert',                  'Convert a lead to an order'),
    ('crm',        'lead.create',                   'Create a lead'),
    ('crm',        'lead.manage',                   'Takeover / reassign leads (manager)'),
    ('crm',        'lead.read',                     'View leads'),
    ('crm',        'lead.write',                    'Edit own lead fields'),
    ('crm',        'plan_change.decide',            'Approve / reject plan change request'),
    ('crm',        'schema.read',                   'View customer schema overrides'),

    -- enterprise
    ('enterprise', 'approval.reassign',             'Re-route an in-flight approval step'),
    ('enterprise', 'approval_template.manage',      'Edit BOQ approval templates'),
    ('enterprise', 'approval_template.read',        'View BOQ approval templates'),
    ('enterprise', 'boq.approve',                   'Approve / reject a BOQ at the approver step'),
    ('enterprise', 'boq.read',                      'View BOQs'),
    ('enterprise', 'boq.submit',                    'Submit a BOQ for approval'),
    ('enterprise', 'boq.write',                     'Edit BOQ lines'),
    ('enterprise', 'ewo.manage',                    'Create / update enterprise work orders'),
    ('enterprise', 'ewo.read',                      'View enterprise work orders'),
    ('enterprise', 'ewo_checklist.manage',          'Edit EWO checklist items'),
    ('enterprise', 'ewo_checklist_template.manage', 'Edit EWO checklist templates'),
    ('enterprise', 'ewo_checklist_template.read',   'View EWO checklist templates'),
    ('enterprise', 'internal_transaction.read',     'View internal procurement transactions'),
    ('enterprise', 'invoice.manage',                'Issue / void enterprise invoices'),
    ('enterprise', 'invoice.read',                  'View enterprise invoices'),
    ('enterprise', 'invoice_plan.manage',           'Edit invoice plan / milestones'),
    ('enterprise', 'negotiation.abort',             'Abort a negotiation'),
    ('enterprise', 'negotiation.activate',          'Activate a negotiation'),
    ('enterprise', 'negotiation.approve',           'Approve a negotiation'),
    ('enterprise', 'negotiation.configure',         'Configure negotiation rounds'),
    ('enterprise', 'negotiation.read',              'View negotiations'),
    ('enterprise', 'negotiation.submit',            'Submit a negotiation round'),
    ('enterprise', 'notification.read',             'View enterprise notification inbox'),
    ('enterprise', 'notification_pref.manage',      'Edit own notification preferences'),
    ('enterprise', 'opportunity.advance',           'Advance opportunity stage'),
    ('enterprise', 'opportunity.read',              'View opportunities'),
    ('enterprise', 'opportunity.write',             'Edit opportunities'),
    ('enterprise', 'payment.record',                'Record enterprise payment'),
    ('enterprise', 'payment_proof.manage',          'Upload / verify payment proofs'),
    ('enterprise', 'po_document.manage',            'Upload / verify PO documents'),
    ('enterprise', 'pricebook.manage',              'Edit pricebooks'),
    ('enterprise', 'pricebook.read',                'View pricebooks'),
    ('enterprise', 'project.manage',                'Edit enterprise projects'),
    ('enterprise', 'project.read',                  'View enterprise projects'),
    ('enterprise', 'quotation.manage',              'Issue / edit quotations'),
    ('enterprise', 'quotation.read',                'View quotations'),
    ('enterprise', 'rfq.manage',                    'Edit RFQs'),
    ('enterprise', 'rfq.read',                      'View RFQs'),
    ('enterprise', 'sla_template.read',             'View SLA templates'),

    -- field
    ('field',      'bast.noc_verify',               'NOC BAST verification gate'),
    ('field',      'cross_area.request',            'Submit cross-area technician request'),
    ('field',      'maintenance.read',              'View maintenance work orders'),
    ('field',      'sla.read',                      'View SLA breach dashboard'),
    ('field',      'team.manage',                   'Edit field teams'),
    ('field',      'team.read',                     'View field teams'),
    ('field',      'tech_location.read',            'View live technician locations'),
    ('field',      'ticket.read',                   'View field tickets'),
    ('field',      'wo.assign',                     'Assign a work order to a team'),
    ('field',      'wo.create',                     'Create a work order'),
    ('field',      'wo.read',                       'View work orders'),
    ('field',      'wo.reschedule',                 'Reschedule a work order'),
    ('field',      'wo.submit_bast',                'Submit BAST on a work order'),
    ('field',      'wo.update',                     'Edit work order fields'),

    -- identity
    ('identity',   'audit.read',                    'View identity audit log'),
    ('identity',   'availability.manage',           'Edit own availability'),
    ('identity',   'availability.read',             'View team availability'),
    ('identity',   'branch.manage',                 'Edit branches'),
    ('identity',   'branch.read',                   'View branches'),
    ('identity',   'role.manage',                   'Edit roles + permissions'),
    ('identity',   'role.read',                     'View roles'),
    ('identity',   'user.create',                   'Create a user'),
    ('identity',   'user.deactivate',               'Deactivate a user'),
    ('identity',   'user.read',                     'View users'),
    ('identity',   'user.update',                   'Edit a user'),

    -- network
    ('network',    'odp.manage',                    'Edit ODP topology'),
    ('network',    'topology.manage',               'Edit network topology'),
    ('network',    'topology.read',                 'View network topology'),

    -- platform
    ('platform',   'schema.manage',                 'Edit platform schema definitions'),
    ('platform',   'schema.read',                   'View platform schema definitions'),
    ('platform',   'schema_override.manage',        'Edit per-customer schema overrides'),
    ('platform',   'schema_override.read',          'View per-customer schema overrides'),
    ('platform',   'webhook_delivery.read',         'View webhook delivery log'),

    -- warehouse
    ('warehouse',  'catalog.manage',                'Edit material catalog'),
    ('warehouse',  'catalog.read',                  'View material catalog'),
    ('warehouse',  'dispatch.read',                 'View dispatches'),
    ('warehouse',  'opname.execute',                'Execute stock opname'),
    ('warehouse',  'opname.read.rollup',            'View opname rollup'),
    ('warehouse',  'stock.intake',                  'Record stock intake'),
    ('warehouse',  'stock.read',                    'View stock balances'),
    ('warehouse',  'stock_dashboard.read',          'View stock dashboard'),
    ('warehouse',  'supplier.manage',               'Edit suppliers'),
    ('warehouse',  'supplier.read',                 'View suppliers'),
    ('warehouse',  'threshold.manage',              'Edit stock thresholds'),
    ('warehouse',  'transfer.manage',               'Record stock transfers'),
    ('warehouse',  'warehouse.manage',              'Edit warehouses'),
    ('warehouse',  'warehouse.read',                'View warehouses')
ON CONFLICT (module, action) DO NOTHING;

-- =====================================================================
-- 2. identity.roles — top-up
--
-- 0001 seeded the canonical 17 roles. Re-assert here so this migration is
-- self-contained (a fresh deploy can run only 0001 + 0047 and still get a
-- working RBAC catalog if any role definitions drift).
-- =====================================================================
INSERT INTO identity.roles (name, description) VALUES
    ('super_admin',        'Full system access'),
    ('operations_admin',   'Branch and user management, platform config'),
    ('product_admin',      'Product catalog, schema builder, WO checklist templates'),
    ('finance_admin',      'Billing schema, commission schema, pricing, tax settings'),
    ('it_admin',           'Integration settings, API keys, notification templates'),
    ('sales_rep',          'Field prospecting, lead and order management, own commission view'),
    ('sales_manager',      'Pipeline oversight, lead takeover, downgrade approval'),
    ('cs_agent',           'Ticket management'),
    ('cs_supervisor',      'CS oversight + termination/plan-change approval'),
    ('noc',                'BAST verification, network monitoring, maintenance WO creation'),
    ('noc_manager',        'NOC + cross-area incident management, War Room'),
    ('finance_staff',      'Invoice processing, payment confirmation'),
    ('finance_manager',    'Finance + suspension approval, schema override'),
    ('warehouse_staff',    'Stock dispatch, QR scanning'),
    ('warehouse_manager',  'Warehouse + threshold management, opname'),
    ('team_leader',        'WO assignment for their area, technician pairing'),
    ('technician',         'WO execution, BAST submission')
ON CONFLICT (name) DO NOTHING;

-- =====================================================================
-- 3. identity.role_permissions — default mapping
--
-- Strategy:
--   * super_admin gets EVERY permission (CROSS JOIN against full catalog)
--   * every other role gets a hand-picked list — read where appropriate,
--     write/manage scoped to the role's domain
--
-- All mappings use ON CONFLICT DO NOTHING. If an operator has already
-- customized a role's permissions, this migration only TOPS UP missing
-- ones; it never revokes.
-- =====================================================================

-- super_admin → all permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
ON CONFLICT DO NOTHING;

-- operations_admin → identity + branches + platform config + audit
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND (p.module, p.action) IN (
    ('identity',  'audit.read'),
    ('identity',  'availability.read'),
    ('identity',  'availability.manage'),
    ('identity',  'branch.read'),
    ('identity',  'branch.manage'),
    ('identity',  'role.read'),
    ('identity',  'role.manage'),
    ('identity',  'user.read'),
    ('identity',  'user.create'),
    ('identity',  'user.update'),
    ('identity',  'user.deactivate'),
    ('admin',     'platform_config.read'),
    ('admin',     'platform_config.manage'),
    ('platform',  'schema.read'),
    ('platform',  'webhook_delivery.read')
  )
ON CONFLICT DO NOTHING;

-- product_admin → product catalog, schemas, EWO templates
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'product_admin'
  AND (p.module, p.action) IN (
    ('platform',   'schema.read'),
    ('platform',   'schema.manage'),
    ('platform',   'schema_override.read'),
    ('platform',   'schema_override.manage'),
    ('crm',        'schema.read'),
    ('enterprise', 'ewo_checklist_template.read'),
    ('enterprise', 'ewo_checklist_template.manage'),
    ('enterprise', 'sla_template.read'),
    ('enterprise', 'approval_template.read'),
    ('enterprise', 'approval_template.manage'),
    ('enterprise', 'pricebook.read'),
    ('enterprise', 'pricebook.manage')
  )
ON CONFLICT DO NOTHING;

-- finance_admin → billing policy, pricing schemas, all billing reads
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_admin'
  AND (p.module, p.action) IN (
    ('billing',    'commission.read'),
    ('billing',    'cycles.read'),
    ('billing',    'invoice.read'),
    ('billing',    'policy.read'),
    ('billing',    'policy.manage'),
    ('billing',    'referral.read'),
    ('billing',    'termination.read'),
    ('billing',    'termination.manage'),
    ('platform',   'schema_override.read'),
    ('platform',   'schema_override.manage'),
    ('enterprise', 'pricebook.read'),
    ('enterprise', 'pricebook.manage')
  )
ON CONFLICT DO NOTHING;

-- it_admin → integrations, webhooks, platform schemas (read)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'it_admin'
  AND (p.module, p.action) IN (
    ('platform',  'webhook_delivery.read'),
    ('platform',  'schema.read'),
    ('admin',     'platform_config.read'),
    ('admin',     'platform_config.manage'),
    ('identity',  'audit.read')
  )
ON CONFLICT DO NOTHING;

-- sales_rep → lead lifecycle + own opportunities
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND (p.module, p.action) IN (
    ('crm',        'dashboard.read'),
    ('crm',        'customer.read'),
    ('crm',        'lead.read'),
    ('crm',        'lead.create'),
    ('crm',        'lead.write'),
    ('crm',        'lead.convert'),
    ('enterprise', 'opportunity.read'),
    ('enterprise', 'opportunity.write'),
    ('enterprise', 'rfq.read'),
    ('enterprise', 'quotation.read'),
    ('enterprise', 'boq.read'),
    ('enterprise', 'boq.write'),
    ('enterprise', 'boq.submit'),
    ('enterprise', 'notification.read'),
    ('enterprise', 'notification_pref.manage'),
    ('identity',   'availability.read')
  )
ON CONFLICT DO NOTHING;

-- sales_manager → sales_rep + takeover + advance + plan change decisions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND (p.module, p.action) IN (
    ('crm',        'dashboard.read'),
    ('crm',        'customer.read'),
    ('crm',        'lead.read'),
    ('crm',        'lead.create'),
    ('crm',        'lead.write'),
    ('crm',        'lead.convert'),
    ('crm',        'lead.manage'),
    ('crm',        'plan_change.decide'),
    ('enterprise', 'opportunity.read'),
    ('enterprise', 'opportunity.write'),
    ('enterprise', 'opportunity.advance'),
    ('enterprise', 'rfq.read'),
    ('enterprise', 'rfq.manage'),
    ('enterprise', 'quotation.read'),
    ('enterprise', 'quotation.manage'),
    ('enterprise', 'boq.read'),
    ('enterprise', 'boq.write'),
    ('enterprise', 'boq.submit'),
    ('enterprise', 'boq.approve'),
    ('enterprise', 'negotiation.read'),
    ('enterprise', 'negotiation.configure'),
    ('enterprise', 'negotiation.activate'),
    ('enterprise', 'negotiation.submit'),
    ('enterprise', 'negotiation.approve'),
    ('enterprise', 'negotiation.abort'),
    ('enterprise', 'project.read'),
    ('enterprise', 'project.manage'),
    ('enterprise', 'approval.reassign'),
    ('enterprise', 'notification.read'),
    ('enterprise', 'notification_pref.manage'),
    ('identity',   'availability.read')
  )
ON CONFLICT DO NOTHING;

-- cs_agent → tickets + customer read + lead read
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'cs_agent'
  AND (p.module, p.action) IN (
    ('field',    'ticket.read'),
    ('crm',      'customer.read'),
    ('crm',      'lead.read'),
    ('billing',  'invoice.read'),
    ('billing',  'termination.read')
  )
ON CONFLICT DO NOTHING;

-- cs_supervisor → cs_agent + termination + plan_change decide
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'cs_supervisor'
  AND (p.module, p.action) IN (
    ('field',    'ticket.read'),
    ('crm',      'customer.read'),
    ('crm',      'lead.read'),
    ('crm',      'plan_change.decide'),
    ('billing',  'invoice.read'),
    ('billing',  'termination.read'),
    ('billing',  'termination.manage')
  )
ON CONFLICT DO NOTHING;

-- noc → BAST verify, topology read, maintenance + tech locations
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc'
  AND (p.module, p.action) IN (
    ('field',    'bast.noc_verify'),
    ('field',    'maintenance.read'),
    ('field',    'wo.read'),
    ('field',    'wo.create'),
    ('field',    'sla.read'),
    ('field',    'tech_location.read'),
    ('network',  'topology.read'),
    ('crm',      'customer.read')
  )
ON CONFLICT DO NOTHING;

-- noc_manager → noc + odp.manage + cross area + topology.manage
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc_manager'
  AND (p.module, p.action) IN (
    ('field',    'bast.noc_verify'),
    ('field',    'cross_area.request'),
    ('field',    'maintenance.read'),
    ('field',    'wo.read'),
    ('field',    'wo.create'),
    ('field',    'wo.assign'),
    ('field',    'sla.read'),
    ('field',    'tech_location.read'),
    ('network',  'topology.read'),
    ('network',  'topology.manage'),
    ('network',  'odp.manage'),
    ('crm',      'customer.read')
  )
ON CONFLICT DO NOTHING;

-- finance_staff → invoices, payments, cycles, commission read
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_staff'
  AND (p.module, p.action) IN (
    ('billing',    'commission.read'),
    ('billing',    'cycles.read'),
    ('billing',    'invoice.read'),
    ('billing',    'invoice.create'),
    ('billing',    'payment.record'),
    ('billing',    'policy.read'),
    ('billing',    'referral.read'),
    ('billing',    'termination.read'),
    ('enterprise', 'invoice.read'),
    ('enterprise', 'invoice.manage'),
    ('enterprise', 'invoice_plan.manage'),
    ('enterprise', 'payment.record'),
    ('enterprise', 'payment_proof.manage')
  )
ON CONFLICT DO NOTHING;

-- finance_manager → finance_staff + cycles.run + invoice.void + policy.manage + termination.manage
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_manager'
  AND (p.module, p.action) IN (
    ('billing',    'commission.read'),
    ('billing',    'cycles.read'),
    ('billing',    'cycles.run'),
    ('billing',    'invoice.read'),
    ('billing',    'invoice.create'),
    ('billing',    'invoice.void'),
    ('billing',    'payment.record'),
    ('billing',    'policy.read'),
    ('billing',    'policy.manage'),
    ('billing',    'referral.read'),
    ('billing',    'termination.read'),
    ('billing',    'termination.manage'),
    ('platform',   'schema_override.read'),
    ('platform',   'schema_override.manage'),
    ('enterprise', 'invoice.read'),
    ('enterprise', 'invoice.manage'),
    ('enterprise', 'invoice_plan.manage'),
    ('enterprise', 'payment.record'),
    ('enterprise', 'payment_proof.manage'),
    ('enterprise', 'internal_transaction.read')
  )
ON CONFLICT DO NOTHING;

-- warehouse_staff → stock read/intake, dispatch read, opname.execute
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_staff'
  AND (p.module, p.action) IN (
    ('warehouse', 'catalog.read'),
    ('warehouse', 'dispatch.read'),
    ('warehouse', 'opname.execute'),
    ('warehouse', 'opname.read.rollup'),
    ('warehouse', 'stock.read'),
    ('warehouse', 'stock.intake'),
    ('warehouse', 'stock_dashboard.read'),
    ('warehouse', 'supplier.read'),
    ('warehouse', 'transfer.manage'),
    ('warehouse', 'warehouse.read')
  )
ON CONFLICT DO NOTHING;

-- warehouse_manager → warehouse_staff + manage catalog/supplier/threshold/warehouse
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND (p.module, p.action) IN (
    ('warehouse', 'catalog.read'),
    ('warehouse', 'catalog.manage'),
    ('warehouse', 'dispatch.read'),
    ('warehouse', 'opname.execute'),
    ('warehouse', 'opname.read.rollup'),
    ('warehouse', 'stock.read'),
    ('warehouse', 'stock.intake'),
    ('warehouse', 'stock_dashboard.read'),
    ('warehouse', 'supplier.read'),
    ('warehouse', 'supplier.manage'),
    ('warehouse', 'threshold.manage'),
    ('warehouse', 'transfer.manage'),
    ('warehouse', 'warehouse.read'),
    ('warehouse', 'warehouse.manage')
  )
ON CONFLICT DO NOTHING;

-- team_leader → assign + reschedule + manage team
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_leader'
  AND (p.module, p.action) IN (
    ('field',    'team.read'),
    ('field',    'team.manage'),
    ('field',    'wo.read'),
    ('field',    'wo.create'),
    ('field',    'wo.assign'),
    ('field',    'wo.reschedule'),
    ('field',    'wo.update'),
    ('field',    'tech_location.read'),
    ('field',    'sla.read'),
    ('field',    'maintenance.read'),
    ('identity', 'availability.read'),
    ('identity', 'availability.manage'),
    ('crm',      'customer.read')
  )
ON CONFLICT DO NOTHING;

-- technician → own WO read/update + BAST submit + own availability
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND (p.module, p.action) IN (
    ('field',    'wo.read'),
    ('field',    'wo.update'),
    ('field',    'wo.submit_bast'),
    ('field',    'team.read'),
    ('identity', 'availability.read'),
    ('identity', 'availability.manage')
  )
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 4. identity.branches — default HQ regional
--
-- A fresh deploy needs at least one branch so the first super_admin user
-- (created out-of-band) can be assigned somewhere. Code 'HQ' is the
-- dedup key — the second `branches_regional_no_parent` CHECK constraint
-- in 0001 enforces that regionals have NULL parent_id.
-- =====================================================================
INSERT INTO identity.branches (name, code, level, parent_id, active) VALUES
    ('Head Office', 'HQ', 'regional', NULL, TRUE)
ON CONFLICT (code) DO NOTHING;

-- =====================================================================
-- 5. crm.products — default broadband plans
--
-- Two starter SKUs so a fresh deploy can capture a lead and convert it to
-- an order. Real product catalog is populated by product_admin later.
-- Prices in IDR (the only currency Phase 1 supports).
-- =====================================================================
INSERT INTO crm.products
    (code,   name,                 speed_mbps, monthly_price, otc_price, temp_activation_window_hours, active) VALUES
    ('BB-10',  'Home 10 Mbps',     10,         149000,        250000,    72,                            TRUE),
    ('BB-30',  'Home 30 Mbps',     30,         249000,        250000,    72,                            TRUE),
    ('BB-50',  'Home 50 Mbps',     50,         349000,        250000,    72,                            TRUE),
    ('BB-100', 'Home 100 Mbps',    100,        499000,        250000,    72,                            TRUE)
ON CONFLICT (code) DO NOTHING;

-- =====================================================================
-- 6. enterprise.approval_templates — default placeholder
--
-- Insert one inactive, unpublished template so admins have an editable
-- starting point in the dashboard. members table stays empty — operator
-- adds approvers via the dashboard once users exist.
-- =====================================================================
INSERT INTO enterprise.approval_templates
    (key,                  name,                              mode,         description,                                            active, published_at) VALUES
    ('APT-BOQ-DEFAULT',    'Default BOQ approval chain',      'sequential', 'Edit to add your approvers. Inactive until published.', FALSE, NULL),
    ('APT-RFQ-DEFAULT',    'Default RFQ approval chain',      'sequential', 'Edit to add your approvers. Inactive until published.', FALSE, NULL)
ON CONFLICT (key) DO NOTHING;

-- =====================================================================
-- 7. enterprise.ewo_checklist_templates — default install + maintenance
--
-- Two starter checklists so the first enterprise WO has *something* to
-- check off. items is jsonb so admins can edit atomically; shape is
-- [{ seq_no, label, description }].
-- =====================================================================
INSERT INTO enterprise.ewo_checklist_templates
    (code,                 name,                          description,                                      active, items) VALUES
    ('EWO-INSTALL-STD',
        'Enterprise install — standard',
        'Default 5-step install checklist for enterprise customers.',
        TRUE,
        '[
            {"seq_no": 1, "label": "Site survey complete",     "description": "GPS, room access, cable route confirmed"},
            {"seq_no": 2, "label": "Cable run + termination",  "description": "Including labelling per BOQ"},
            {"seq_no": 3, "label": "CPE provisioning",         "description": "Power on, factory reset, firmware check"},
            {"seq_no": 4, "label": "Speed test + BAST sign",   "description": "≥ 80% of contracted speed at termination"},
            {"seq_no": 5, "label": "Customer acceptance",      "description": "Signature + photo of BAST page"}
        ]'::jsonb),
    ('EWO-MAINT-STD',
        'Enterprise maintenance — standard',
        'Default 4-step maintenance checklist.',
        TRUE,
        '[
            {"seq_no": 1, "label": "Pre-visit ticket review",   "description": "Symptom, customer ticket, last BAST"},
            {"seq_no": 2, "label": "On-site inspection",        "description": "CPE, cabling, signal levels"},
            {"seq_no": 3, "label": "Root cause + remediation",  "description": "Recorded for trend analysis"},
            {"seq_no": 4, "label": "Customer sign-off",         "description": "Signature + restored speed test"}
        ]'::jsonb)
ON CONFLICT (code) DO NOTHING;

COMMIT;
