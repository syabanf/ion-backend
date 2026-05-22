-- M3 Round 2 — Stock thresholds (alerts) + opname workflow.
--
-- Round-2 scope:
--   * Threshold-driven alerts: `min_threshold` already exists on
--     stock_levels (from M3 r1). We expose set/get + an alerts query
--     that aggregates below-threshold items for a user's branch + all
--     parent branches (PRD requires escalation to parent levels).
--   * Opname workflow: a session captures a physical count of items in
--     a warehouse. Each line records expected vs counted; variance
--     becomes an `opname_adjustment` stock_movement on commit. Cable
--     items get a dedicated remnant decision (keep_partial / scrap).
--
-- Deferred to round-3 (not created here):
--   * Retrofit / cannibalization (asset → asset parts harvest, PRD §8A)
--   * FIFO/LIFO valuation method switch (read from platform_config)
--   * Warehouse dispatch QR flow (cross-cut with M5)
--   * WO return paths

-- =====================================================================
-- 1. Opname sessions
--
-- A session is "we walked into warehouse X with a clipboard". It can
-- include many lines (one per item we counted). The session itself is
-- the audit trail row that ties counts together.
-- =====================================================================
CREATE TABLE warehouse.opname_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_number  TEXT NOT NULL UNIQUE,
    warehouse_id    UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    status          TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'committed', 'cancelled')),
    started_by      UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    committed_at    TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_opname_sessions_wh     ON warehouse.opname_sessions (warehouse_id);
CREATE INDEX idx_opname_sessions_status ON warehouse.opname_sessions (status);

-- At most one *open* session per warehouse — keeps the count workflow
-- predictable. Committed/cancelled sessions stack up freely as history.
CREATE UNIQUE INDEX uniq_open_opname_per_warehouse
    ON warehouse.opname_sessions (warehouse_id)
    WHERE status = 'open';

-- =====================================================================
-- 2. Opname counts — one line per (session, stock_item)
--
-- The counter enters `counted_qty`. We persist `expected_qty` at line
-- creation time so a later view of the session shows what the system
-- thought was on hand at count time, even after movements have shifted
-- live numbers. `variance` is derived (counted - expected) but stored
-- as a column to keep the schema simple for reports.
--
-- For cables (sold by meter), short remnants are common; the counter
-- decides on `cable_remnant_decision`:
--   - keep_partial — accept the remnant; live stock_level becomes counted
--   - scrap         — write off the remnant; live stock_level becomes 0
-- For non-cable items the column stays NULL.
-- =====================================================================
CREATE TABLE warehouse.opname_counts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES warehouse.opname_sessions(id) ON DELETE CASCADE,
    stock_item_id   UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    expected_qty    NUMERIC(18, 3) NOT NULL,
    counted_qty     NUMERIC(18, 3) NOT NULL CHECK (counted_qty >= 0),
    variance        NUMERIC(18, 3) NOT NULL, -- counted - expected
    cable_remnant_decision TEXT
        CHECK (cable_remnant_decision IN ('keep_partial', 'scrap')),
    notes           TEXT,
    counted_by      UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    counted_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, stock_item_id)
);
CREATE INDEX idx_opname_counts_session ON warehouse.opname_counts (session_id);
CREATE INDEX idx_opname_counts_item    ON warehouse.opname_counts (stock_item_id);

-- =====================================================================
-- 3. Permission seeds
--
-- warehouse.opname.execute and warehouse.threshold.manage already exist
-- from M3 r1 seeds in 0006 (sanity-check assumed). We add only what's
-- missing: a "see alerts" permission distinct from generic stock.read.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('warehouse', 'threshold.manage', 'Set min_threshold per (warehouse, item)'),
    ('warehouse', 'alerts.read', 'View below-threshold alerts (with parent-branch escalation)')
ON CONFLICT (module, action) DO NOTHING;

-- Grant new perms to existing roles. We keep the seed pattern from r1:
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'warehouse'
  AND p.action IN ('threshold.manage','alerts.read')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND p.module = 'warehouse'
  AND p.action IN ('threshold.manage','alerts.read')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('warehouse_staff','operations_admin')
  AND p.module = 'warehouse'
  AND p.action = 'alerts.read'
ON CONFLICT DO NOTHING;
