-- 0033_wo_dispatch.up.sql
--
-- Warehouse → Work Order dispatch flow. The Round-1 warehouse module
-- only supported inter-warehouse transfers; this round adds the
-- "technician picks up gear against a WO via BOM + QR scan" path the
-- PRD requires for field installations.
--
-- Lifecycle:
--   planned     → created with a BOM (list of items+qty), nothing reserved yet
--   staged      → stockkeeper has the gear gathered + waiting at the counter
--   picked_up   → all items scanned + handed to the technician
--   returned    → leftover / unused items handed back (partial allowed)
--   cancelled   → planned/staged only — gear never left the warehouse
--
-- Stock-level deltas + asset.status flips are NOT applied in this
-- migration. They live in the usecase layer (same pattern as transfers,
-- so the audit trail stays consistent). This migration is purely the
-- table + index + permission scaffolding.

BEGIN;

-- =====================================================================
-- warehouse.wo_dispatch_records — header per (warehouse, WO) dispatch
-- =====================================================================
-- wo_id is a SOFT foreign key. field.work_orders lives in a different
-- service binary; we don't want the warehouse module to fail loading if
-- the field service hasn't run yet. The application layer is responsible
-- for verifying the WO exists.
CREATE TABLE IF NOT EXISTS warehouse.wo_dispatch_records (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    wo_id           UUID NOT NULL,
    warehouse_id    UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    dispatched_by   UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'planned'
        CHECK (status IN ('planned','staged','picked_up','returned','cancelled')),
    planned_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    staged_at       TIMESTAMPTZ,
    picked_up_at    TIMESTAMPTZ,
    returned_at     TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ,
    cancel_reason   TEXT NOT NULL DEFAULT '',
    notes           TEXT NOT NULL DEFAULT '',
    -- revision counter — bumped on item add/remove so the UI can detect
    -- stale BOMs and prompt the technician to re-fetch before scanning.
    revision        INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_wo_dispatch_wo
    ON warehouse.wo_dispatch_records(wo_id);
CREATE INDEX IF NOT EXISTS idx_wo_dispatch_warehouse_status
    ON warehouse.wo_dispatch_records(warehouse_id, status);
CREATE INDEX IF NOT EXISTS idx_wo_dispatch_status_planned_at
    ON warehouse.wo_dispatch_records(status, planned_at DESC);

-- =====================================================================
-- warehouse.wo_dispatch_items — BOM line + scan state
-- =====================================================================
-- One row per item the BOM expects. `qty` is the planned quantity;
-- `serial_or_qr` is populated when a technician scans the unit (so
-- non-serialized lines leave it NULL even after pickup). `returned_qty`
-- is the running tally so callers can do partial returns without
-- introducing a separate return-items table.
CREATE TABLE IF NOT EXISTS warehouse.wo_dispatch_items (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    dispatch_id     UUID NOT NULL REFERENCES warehouse.wo_dispatch_records(id) ON DELETE CASCADE,
    item_id         UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    qty             NUMERIC(18, 3) NOT NULL CHECK (qty > 0),
    returned_qty    NUMERIC(18, 3) NOT NULL DEFAULT 0 CHECK (returned_qty >= 0),
    serial_or_qr    TEXT,
    status          TEXT NOT NULL DEFAULT 'planned'
        CHECK (status IN ('planned','picked','returned')),
    picked_at       TIMESTAMPTZ,
    picked_by       UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    notes           TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_wo_dispatch_items_dispatch_status
    ON warehouse.wo_dispatch_items(dispatch_id, status);
CREATE INDEX IF NOT EXISTS idx_wo_dispatch_items_item
    ON warehouse.wo_dispatch_items(item_id);

-- Idempotency target for PickUpItemByScan — the same (dispatch, item,
-- serial) pair should produce one row, never duplicates. Partial index
-- so multiple non-serialized rows (NULL serial) on the same line don't
-- collide.
CREATE UNIQUE INDEX IF NOT EXISTS uq_wo_dispatch_items_scan
    ON warehouse.wo_dispatch_items(dispatch_id, item_id, serial_or_qr)
    WHERE serial_or_qr IS NOT NULL;

-- =====================================================================
-- updated_at trigger on the header — re-use the existing function from
-- migration 0006.
-- =====================================================================
CREATE TRIGGER trg_wo_dispatch_records_touch
    BEFORE UPDATE ON warehouse.wo_dispatch_records
    FOR EACH ROW EXECUTE FUNCTION warehouse.touch_updated_at();

-- =====================================================================
-- Permissions
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('warehouse', 'dispatch.read',   'View WO dispatch records'),
    ('warehouse', 'dispatch.manage', 'Create / stage / cancel / mark-picked WO dispatches'),
    ('warehouse', 'dispatch.scan',   'Scan QR/serials against a WO dispatch (technician)')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin gets the full set.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'warehouse'
  AND p.action IN ('dispatch.read','dispatch.manage','dispatch.scan')
ON CONFLICT DO NOTHING;

-- warehouse_manager: read + manage (stages and signs off dispatches).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND p.module = 'warehouse'
  AND p.action IN ('dispatch.read','dispatch.manage','dispatch.scan')
ON CONFLICT DO NOTHING;

-- warehouse_staff: read + scan (counter clerk hands gear over after
-- scanning each unit). The PRD allows staff to stage but the management
-- of a dispatch (cancellation, sign-off) is a manager-level call.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_staff'
  AND p.module = 'warehouse'
  AND p.action IN ('dispatch.read','dispatch.scan','dispatch.manage')
ON CONFLICT DO NOTHING;

COMMIT;
