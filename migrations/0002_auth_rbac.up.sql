-- 0002 — Auth (refresh tokens) + RBAC seed (permissions + role→permission grants)
--
-- Strategy:
--   - Access tokens: short-lived JWTs (stateless, verified locally by each service).
--   - Refresh tokens: opaque random strings, stored as bcrypt hashes in
--     identity.refresh_tokens. Rotated on every /refresh. Revocable via /logout.
--   - Permissions: canonical (module, action) pairs. Roles are bundles of these.
--     RBAC checks at the HTTP middleware layer resolve a user's roles → permissions
--     at request time (DB lookup; can be cached in-memory later).

BEGIN;

-- ----------------------------------------------------------------------
-- Refresh tokens — one row per active session.
-- ----------------------------------------------------------------------
CREATE TABLE identity.refresh_tokens (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,                 -- bcrypt(plain_refresh_token)
    issued_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,                   -- non-null = revoked
    replaced_by UUID REFERENCES identity.refresh_tokens(id) ON DELETE SET NULL,
    user_agent  TEXT,                          -- optional device fingerprint
    ip          INET,
    UNIQUE (id)
);

CREATE INDEX idx_refresh_tokens_user_active
    ON identity.refresh_tokens (user_id)
    WHERE revoked_at IS NULL;

-- ----------------------------------------------------------------------
-- Canonical permission catalog.
--
-- Convention: (module, action) where action follows "<resource>.<verb>".
-- Examples:
--   identity, user.read
--   identity, user.create
--   crm,      lead.read
--   crm,      lead.takeover
--
-- Add new permissions here as bounded contexts come online. Seeding new
-- permissions does NOT automatically grant them to any role — explicit
-- grants are required.
-- ----------------------------------------------------------------------
INSERT INTO identity.permissions (module, action, description) VALUES
    -- Identity / users
    ('identity', 'user.read',          'View user records'),
    ('identity', 'user.create',        'Provision new users'),
    ('identity', 'user.update',        'Edit user records'),
    ('identity', 'user.deactivate',    'Mark users inactive'),
    ('identity', 'role.read',          'View roles'),
    ('identity', 'role.assign',        'Assign roles to users'),
    ('identity', 'role.manage',        'Create/edit/delete roles and their permissions'),
    ('identity', 'permission.read',    'View the permission catalog'),
    ('identity', 'branch.read',        'View branches'),
    ('identity', 'branch.manage',      'Create/edit branches'),
    ('identity', 'audit.read',         'Read the audit log'),

    -- Admin / schemas / catalog
    ('admin',    'schema.read',        'View schema definitions'),
    ('admin',    'schema.draft',       'Create/edit schema drafts'),
    ('admin',    'schema.publish',     'Publish schema versions'),
    ('admin',    'product.read',       'View product catalog'),
    ('admin',    'product.manage',     'Edit product catalog'),
    ('admin',    'platform_config.read',   'View platform configuration'),
    ('admin',    'platform_config.manage', 'Edit platform configuration'),

    -- CRM & Sales
    ('crm',      'lead.read',          'View leads'),
    ('crm',      'lead.create',        'Create leads'),
    ('crm',      'lead.update',        'Edit leads'),
    ('crm',      'lead.takeover',      'Take over leads (manager)'),
    ('crm',      'lead.convert',       'Convert a lead to a customer'),
    ('crm',      'customer.read',      'View customer records'),
    ('crm',      'customer.update',    'Edit customer records'),
    ('crm',      'order.read',         'View orders'),
    ('crm',      'order.create',       'Create orders'),

    -- Billing & Finance
    ('billing',  'invoice.read',       'View invoices'),
    ('billing',  'invoice.generate',   'Generate/request invoices'),
    ('billing',  'payment.confirm',    'Confirm payments (manual / bank transfer)'),
    ('billing',  'commission.read',    'Read commission data'),
    ('billing',  'suspension.approve', 'Approve manual suspensions'),
    ('billing',  'schema.override',    'Override billing schema per customer'),

    -- Network & NOC
    ('network',  'topology.read',      'View network topology'),
    ('network',  'topology.manage',    'Edit topology nodes/ports'),
    ('network',  'bast.verify',        'Verify BAST submissions'),
    ('network',  'odp.manage',         'Edit ODP records / coverage'),

    -- Warehouse & Asset
    ('warehouse', 'stock.read',        'View stock levels'),
    ('warehouse', 'stock.dispatch',    'Dispatch stock for WOs'),
    ('warehouse', 'stock.return',      'Receive returned stock'),
    ('warehouse', 'opname.execute',    'Execute stock opname'),

    -- Field / WO
    ('field',    'wo.read',            'View work orders'),
    ('field',    'wo.assign',          'Assign technicians to WOs (team leader)'),
    ('field',    'wo.execute',         'Execute WO from Technical App (technician)'),
    ('field',    'bast.submit',        'Submit BAST from Technical App')
ON CONFLICT (module, action) DO NOTHING;

-- ----------------------------------------------------------------------
-- Seed role → permission grants.
--
-- super_admin gets every permission. Other roles get the minimum sane set;
-- expect to revise these as the UI surfaces individual permission management.
-- ----------------------------------------------------------------------

-- super_admin → all permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
ON CONFLICT DO NOTHING;

-- operations_admin: identity + branches + audit (no schemas/billing)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND (
       (p.module = 'identity' AND p.action IN ('user.read','user.create','user.update','user.deactivate','role.read','role.assign','branch.read','branch.manage','audit.read'))
    OR (p.module = 'admin'    AND p.action IN ('platform_config.read','platform_config.manage'))
  )
ON CONFLICT DO NOTHING;

-- product_admin: schemas + product catalog
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'product_admin'
  AND p.module = 'admin'
ON CONFLICT DO NOTHING;

-- finance_admin: billing schema + commission + billing data
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_admin'
  AND (p.module = 'billing' OR (p.module = 'admin' AND p.action LIKE 'schema.%'))
ON CONFLICT DO NOTHING;

-- sales_rep: read own leads + create leads/orders
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_rep'
  AND p.module = 'crm'
  AND p.action IN ('lead.read','lead.create','lead.update','lead.convert','customer.read','order.read','order.create')
ON CONFLICT DO NOTHING;

-- sales_manager: sales_rep + takeover + branch read
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'sales_manager'
  AND (
       (p.module = 'crm' AND p.action IN ('lead.read','lead.create','lead.update','lead.takeover','lead.convert','customer.read','customer.update','order.read','order.create'))
    OR (p.module = 'identity' AND p.action = 'branch.read')
  )
ON CONFLICT DO NOTHING;

-- noc: bast verify + topology read/manage + odp manage
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc'
  AND p.module = 'network'
ON CONFLICT DO NOTHING;

-- warehouse_staff: stock dispatch + return
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_staff'
  AND p.module = 'warehouse'
  AND p.action IN ('stock.read','stock.dispatch','stock.return')
ON CONFLICT DO NOTHING;

-- warehouse_manager: warehouse_staff + opname
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND p.module = 'warehouse'
ON CONFLICT DO NOTHING;

-- team_leader: WO read + assign
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_leader'
  AND p.module = 'field'
  AND p.action IN ('wo.read','wo.assign')
ON CONFLICT DO NOTHING;

-- technician: WO read/execute + BAST submit
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'field'
  AND p.action IN ('wo.read','wo.execute','bast.submit')
ON CONFLICT DO NOTHING;

-- finance_staff: invoices + payments + commission
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_staff'
  AND p.module = 'billing'
  AND p.action IN ('invoice.read','invoice.generate','payment.confirm','commission.read')
ON CONFLICT DO NOTHING;

-- finance_manager: finance_staff + suspension approval + schema override
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_manager'
  AND p.module = 'billing'
ON CONFLICT DO NOTHING;

COMMIT;
