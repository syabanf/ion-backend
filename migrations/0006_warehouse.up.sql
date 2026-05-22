-- 0006 — Warehouse & Asset module (M3 foundation).
--
-- Creates the `warehouse` schema and the five core tables. Round-1 scope
-- covers: warehouses, stock catalog, per-warehouse stock levels for
-- non-serialized items, serialized asset registry, full audit trail of
-- movements, and inter-warehouse transfers.
--
-- Opname, threshold-escalation, and asset retrofit each warrant their own
-- migration and aren't included here — kept on the round-2 list.

BEGIN;

CREATE SCHEMA IF NOT EXISTS warehouse;

-- ----------------------------------------------------------------------
-- Warehouses — physical storage locations. Belong to a branch (any level)
-- per PRD §Branch Hierarchy. One branch can have many warehouses; one
-- warehouse can in practice serve multiple branches ("shared warehouse
-- model") but we keep the primary FK simple for now and add a junction
-- table later if it becomes load-bearing.
-- ----------------------------------------------------------------------
CREATE TABLE warehouse.warehouses (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL,
    code        TEXT NOT NULL UNIQUE,
    branch_id   UUID REFERENCES identity.branches(id) ON DELETE RESTRICT,
    address     TEXT,
    notes       TEXT,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_warehouses_branch ON warehouse.warehouses(branch_id);

-- ----------------------------------------------------------------------
-- Stock catalog — the canonical "what we stock" registry, one row per
-- distinct item type. Four categories, each with its own tracking rules
-- (see PRD §Warehouse §4).
--
--   serialized_device    — one DB row per UNIT (in warehouse.assets)
--   cable                — length-based; tracked as meters in stock_levels
--   consumable           — count-based; tracked as integer count
--   infrastructure       — like serialized_device but deployed to net sites
--
-- `serialized = TRUE` is the dispatch rule: serialized rows always have
-- per-unit asset records; non-serialized aggregate into stock_levels.
-- ----------------------------------------------------------------------
CREATE TABLE warehouse.stock_items (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    sku                TEXT NOT NULL UNIQUE,
    name               TEXT NOT NULL,
    category           TEXT NOT NULL CHECK (category IN ('serialized_device','cable','consumable','infrastructure')),
    brand              TEXT,
    model              TEXT,
    spec               TEXT,
    unit               TEXT NOT NULL CHECK (unit IN ('pcs','meters','pack')),
    serialized         BOOLEAN NOT NULL,
    default_unit_cost  NUMERIC(14, 2),
    active             BOOLEAN NOT NULL DEFAULT TRUE,
    metadata           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Belt and braces: serialized iff category is serialized_device or
    -- infrastructure. Lets us avoid asking the question twice.
    CONSTRAINT stock_items_serialized_consistency
        CHECK ((category IN ('serialized_device','infrastructure')) = serialized)
);

CREATE INDEX idx_stock_items_category ON warehouse.stock_items(category);

-- ----------------------------------------------------------------------
-- Stock levels — non-serialized aggregate per (warehouse, item).
-- Cable: quantity in meters; consumable: quantity in pieces.
-- min_threshold drives the threshold alert flow (round 2).
-- ----------------------------------------------------------------------
CREATE TABLE warehouse.stock_levels (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    warehouse_id   UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE CASCADE,
    stock_item_id  UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    quantity       NUMERIC(18, 3) NOT NULL DEFAULT 0,
    min_threshold  NUMERIC(18, 3),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (warehouse_id, stock_item_id),
    CHECK (quantity >= 0)
);

CREATE INDEX idx_stock_levels_warehouse ON warehouse.stock_levels(warehouse_id);

-- ----------------------------------------------------------------------
-- Assets — per-unit registry for serialized items (Type 1 + Type 4).
--
-- Lifecycle status (PRD §4.1):
--   in_stock       — sitting in the warehouse
--   dispatched     — assigned to a WO, not yet installed
--   installed      — at a customer site / deployed to a network node
--   returned       — back from the field, condition pending review
--   decommissioned — written off
--   cannibalized   — disassembled in a retrofit (round 2)
--
-- `warehouse_id` is NULLABLE: once installed/deployed, the asset is no
-- longer in a warehouse. Same for `customer_id` / `network_node_id` —
-- present only when relevant.
--
-- received_at + purchase_cost are mandatory at intake per PRD inventory-
-- valuation requirement — they drive FIFO/LIFO dispatch.
-- ----------------------------------------------------------------------
CREATE TABLE warehouse.assets (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stock_item_id            UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    warehouse_id             UUID REFERENCES warehouse.warehouses(id) ON DELETE SET NULL,
    serial_number            TEXT UNIQUE,
    qr_code                  TEXT UNIQUE,
    mac_address              TEXT,
    firmware_version         TEXT,
    ownership_type           TEXT NOT NULL DEFAULT 'ion_owned'
        CHECK (ownership_type IN ('ion_owned','leased_to_customer','customer_owned')),
    condition                TEXT NOT NULL DEFAULT 'new'
        CHECK (condition IN ('new','refurbished','damaged')),
    status                   TEXT NOT NULL DEFAULT 'in_stock'
        CHECK (status IN ('in_stock','dispatched','installed','returned','decommissioned','cannibalized','deployed')),
    received_at              TIMESTAMPTZ NOT NULL,
    purchase_cost            NUMERIC(14, 2),
    purchase_date            DATE,
    distributor              TEXT,
    purchase_order_ref       TEXT,
    warranty_expiry          DATE,
    is_retrofit              BOOLEAN NOT NULL DEFAULT FALSE,
    customer_id              UUID,   -- FK to crm.customers (added when CRM lands)
    assigned_technician_id   UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    wo_id                    UUID,   -- FK to field.work_orders (added later)
    network_node_id          UUID REFERENCES network.nodes(id) ON DELETE SET NULL,
    notes                    TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_assets_stock_item     ON warehouse.assets(stock_item_id);
CREATE INDEX idx_assets_warehouse      ON warehouse.assets(warehouse_id) WHERE warehouse_id IS NOT NULL;
CREATE INDEX idx_assets_status         ON warehouse.assets(status);
-- FIFO/LIFO dispatch suggestion is `ORDER BY received_at` with the same
-- (warehouse, stock_item) filter; this index makes it index-only.
CREATE INDEX idx_assets_fifo_lifo
    ON warehouse.assets(warehouse_id, stock_item_id, received_at)
    WHERE status = 'in_stock';

-- ----------------------------------------------------------------------
-- Stock movements — append-only audit of every quantity change.
--
-- For serialized items, asset_id is set and quantity is +1 / -1.
-- For non-serialized, asset_id is NULL and quantity is the meters/count.
--
-- reference_type + reference_id give context for the movement
-- ('transfer', 'wo', 'opname', 'intake', etc.) and let us reconstruct
-- the audit trail when something looks wrong.
-- ----------------------------------------------------------------------
CREATE TABLE warehouse.stock_movements (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    warehouse_id    UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    stock_item_id   UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    asset_id        UUID REFERENCES warehouse.assets(id) ON DELETE SET NULL,
    movement_type   TEXT NOT NULL CHECK (movement_type IN (
                        'intake', 'dispatch', 'return',
                        'transfer_out', 'transfer_in',
                        'opname_adjustment', 'retrofit_consume', 'retrofit_produce',
                        'dispose'
                    )),
    quantity        NUMERIC(18, 3) NOT NULL,
    reason          TEXT,
    reference_type  TEXT,
    reference_id    UUID,
    performed_by    UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    performed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_movements_warehouse_time ON warehouse.stock_movements(warehouse_id, performed_at DESC);
CREATE INDEX idx_movements_item           ON warehouse.stock_movements(stock_item_id);
CREATE INDEX idx_movements_reference      ON warehouse.stock_movements(reference_type, reference_id)
    WHERE reference_id IS NOT NULL;

-- ----------------------------------------------------------------------
-- Inter-warehouse transfers (PRD §10).
--
-- Lifecycle:
--   draft        → just created, can edit items
--   dispatched   → source warehouse confirmed shipment (stock_movements 'transfer_out' written)
--   received     → destination confirmed receipt (stock_movements 'transfer_in' written)
--   cancelled    → never dispatched
--
-- transfer_items holds the per-line quantities + (optionally) specific
-- serialized assets to move. For non-serialized, asset_id is NULL and
-- quantity is meters/count.
-- ----------------------------------------------------------------------
CREATE TABLE warehouse.transfers (
    id                     UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    transfer_number        TEXT NOT NULL UNIQUE,
    source_warehouse_id    UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    destination_warehouse_id UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    status                 TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','dispatched','received','cancelled')),
    notes                  TEXT,
    created_by             UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    dispatched_at          TIMESTAMPTZ,
    received_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (source_warehouse_id <> destination_warehouse_id)
);

CREATE TABLE warehouse.transfer_items (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    transfer_id    UUID NOT NULL REFERENCES warehouse.transfers(id) ON DELETE CASCADE,
    stock_item_id  UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    asset_id       UUID REFERENCES warehouse.assets(id) ON DELETE SET NULL,
    quantity       NUMERIC(18, 3) NOT NULL CHECK (quantity > 0)
);

CREATE INDEX idx_transfer_items_transfer ON warehouse.transfer_items(transfer_id);

-- ----------------------------------------------------------------------
-- updated_at triggers
-- ----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION warehouse.touch_updated_at() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_warehouses_touch
    BEFORE UPDATE ON warehouse.warehouses
    FOR EACH ROW EXECUTE FUNCTION warehouse.touch_updated_at();

CREATE TRIGGER trg_stock_items_touch
    BEFORE UPDATE ON warehouse.stock_items
    FOR EACH ROW EXECUTE FUNCTION warehouse.touch_updated_at();

CREATE TRIGGER trg_assets_touch
    BEFORE UPDATE ON warehouse.assets
    FOR EACH ROW EXECUTE FUNCTION warehouse.touch_updated_at();

CREATE TRIGGER trg_transfers_touch
    BEFORE UPDATE ON warehouse.transfers
    FOR EACH ROW EXECUTE FUNCTION warehouse.touch_updated_at();

-- ----------------------------------------------------------------------
-- New permission seeds — extend the catalog seeded in migration 0002.
-- ----------------------------------------------------------------------
INSERT INTO identity.permissions (module, action, description) VALUES
    ('warehouse', 'warehouse.read',    'View warehouses'),
    ('warehouse', 'warehouse.manage',  'Create/edit warehouses'),
    ('warehouse', 'catalog.read',      'View the stock catalog'),
    ('warehouse', 'catalog.manage',    'Create/edit stock items'),
    ('warehouse', 'stock.intake',      'Receive new stock into a warehouse'),
    ('warehouse', 'transfer.manage',   'Create / dispatch / receive inter-warehouse transfers')
ON CONFLICT (module, action) DO NOTHING;

-- Grant the new perms to existing roles.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'warehouse'
  AND p.action IN ('warehouse.read','warehouse.manage')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name IN ('warehouse_staff','warehouse_manager')
  AND p.module = 'warehouse'
  AND p.action IN ('warehouse.read','catalog.read','stock.intake')
ON CONFLICT DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND p.module = 'warehouse'
  AND p.action IN ('catalog.manage','transfer.manage')
ON CONFLICT DO NOTHING;

COMMIT;
