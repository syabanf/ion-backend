-- Wave 117 — Warehouse depth (Phase 1B operationalization).
--
-- Closes the residual warehouse + asset gaps from the Wave 110 audit:
--   • Item Type taxonomy (Type 1 serialized / Type 2 cable / Type 3 consumable / Type 4 infra)
--   • Configurable item categories (replaces the hardcoded enum on stock_items.category)
--   • Cable lots (length-tracked drums) + per-cut audit
--   • Consumable batches (FIFO bulk tracking) + per-WO consumption log
--   • Sub-warehouses (NOC + TL stockholder model under a parent warehouse)
--   • Asset location history (per-asset audit trail of every move)
--   • Stock opname tablet sessions (offline payload sync + reconcile)
--   • QR code column on warehouse.items
--   • Manual Purchase Entry adjuncts (can_purchase flag, import session)
--
-- Bounded-context rule: everything additive lives in the `warehouse` schema.
-- Cross-context references (wo_id, customer_id, sub_warehouse_id when Mobile)
-- are plain UUIDs — no FK across schemas, same convention as Wave 113.

-- =====================================================================
-- 1. Item categories — configurable replacement for the hardcoded enum
-- =====================================================================
--
-- The existing `warehouse.stock_items.category` text+CHECK lives on; this
-- table is the configurable layer on top. New items pick a category, the
-- category supplies item_type + defaults, and stock_items.category is
-- back-filled to the legacy enum for compatibility. type_code is the
-- new four-bucket taxonomy used by all downstream typed flows.
CREATE TABLE warehouse.item_categories (
    id                              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code                            TEXT NOT NULL UNIQUE,
    name                            TEXT NOT NULL,
    parent_id                       UUID REFERENCES warehouse.item_categories(id) ON DELETE SET NULL,
    type_code                       TEXT NOT NULL
        CHECK (type_code IN ('type1','type2','type3','type4')),
    description                     TEXT,
    default_unit                    TEXT,
    sub_warehouse_allowed_default   BOOLEAN NOT NULL DEFAULT TRUE,
    requires_serial_at_intake       BOOLEAN NOT NULL DEFAULT FALSE,
    active                          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_item_categories_parent ON warehouse.item_categories(parent_id);
CREATE INDEX idx_item_categories_type   ON warehouse.item_categories(type_code);

-- =====================================================================
-- 2. stock_items extension — link to category + new typed columns
-- =====================================================================
--
-- ALTER is additive only; existing rows keep their legacy `category` enum
-- value, and the new columns default to safe values so the migration
-- doesn't break existing reads. The category_id FK is nullable so legacy
-- items aren't forced to migrate before a UI is ready.
ALTER TABLE warehouse.stock_items
    ADD COLUMN IF NOT EXISTS category_id           UUID REFERENCES warehouse.item_categories(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS item_type             TEXT NOT NULL DEFAULT 'type1'
        CHECK (item_type IN ('type1','type2','type3','type4')),
    ADD COLUMN IF NOT EXISTS tracking_kind         TEXT NOT NULL DEFAULT 'serialized'
        CHECK (tracking_kind IN ('serialized','length','bulk_quantity','asset_with_location')),
    ADD COLUMN IF NOT EXISTS min_stock_threshold   NUMERIC(18,3) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS qr_code               TEXT,
    ADD COLUMN IF NOT EXISTS barcode_format        TEXT NOT NULL DEFAULT 'qr',
    ADD COLUMN IF NOT EXISTS sub_warehouse_allowed BOOLEAN NOT NULL DEFAULT TRUE;

-- Per-item QR is unique when set (NULL allowed for legacy rows).
CREATE UNIQUE INDEX IF NOT EXISTS uq_stock_items_qr_code
    ON warehouse.stock_items(qr_code) WHERE qr_code IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_stock_items_item_type
    ON warehouse.stock_items(item_type);

-- =====================================================================
-- 3. assets extension — denormalized location + last move timestamp
-- =====================================================================
--
-- current_location_id is the denormalized "where is this asset right now"
-- pointer that mirrors the latest asset_location_history row. We carry it
-- so the per-warehouse inventory query stays index-only (no per-asset
-- JOIN-LATERAL into the history table).
ALTER TABLE warehouse.assets
    ADD COLUMN IF NOT EXISTS manufactured_date   DATE,
    ADD COLUMN IF NOT EXISTS last_movement_at    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS current_location_id UUID;

CREATE INDEX IF NOT EXISTS idx_assets_current_location
    ON warehouse.assets(current_location_id) WHERE current_location_id IS NOT NULL;

-- =====================================================================
-- 4. cable_lots — Type 2 length-tracked drums
-- =====================================================================
--
-- Each row is one physical drum/spool of cable. total_length is the
-- intake number; remaining_length decrements on every cut. Status flows
-- in_stock → allocated → consumed; disposed terminal from anywhere.
CREATE TABLE warehouse.cable_lots (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    item_id                  UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    lot_number               TEXT,
    total_length_meters      NUMERIC(10, 2) NOT NULL CHECK (total_length_meters >= 0),
    remaining_length_meters  NUMERIC(10, 2) NOT NULL CHECK (remaining_length_meters >= 0),
    drum_serial              TEXT,
    supplier_id              UUID,
    received_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status                   TEXT NOT NULL DEFAULT 'in_stock'
        CHECK (status IN ('in_stock','allocated','consumed','disposed')),
    current_warehouse_id     UUID REFERENCES warehouse.warehouses(id) ON DELETE SET NULL,
    unit_cost_per_meter      NUMERIC(14, 4),
    notes                    TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (remaining_length_meters <= total_length_meters)
);

CREATE INDEX idx_cable_lots_item_status      ON warehouse.cable_lots(item_id, status);
CREATE INDEX idx_cable_lots_drum_serial      ON warehouse.cable_lots(drum_serial) WHERE drum_serial IS NOT NULL;
CREATE INDEX idx_cable_lots_warehouse_status ON warehouse.cable_lots(current_warehouse_id, status);
CREATE INDEX idx_cable_lots_fifo             ON warehouse.cable_lots(item_id, received_at) WHERE status = 'in_stock';

-- =====================================================================
-- 5. cable_cuts — immutable audit row per cut from a lot
-- =====================================================================
CREATE TABLE warehouse.cable_cuts (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    cable_lot_id        UUID NOT NULL REFERENCES warehouse.cable_lots(id) ON DELETE RESTRICT,
    cut_length_meters   NUMERIC(10, 2) NOT NULL CHECK (cut_length_meters > 0),
    used_for_wo_id      UUID,
    cut_by              UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    cut_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notes               TEXT
);

CREATE INDEX idx_cable_cuts_lot_time ON warehouse.cable_cuts(cable_lot_id, cut_at DESC);
CREATE INDEX idx_cable_cuts_wo       ON warehouse.cable_cuts(used_for_wo_id) WHERE used_for_wo_id IS NOT NULL;

-- =====================================================================
-- 6. consumable_batches — Type 3 bulk-qty batches with FIFO ordering
-- =====================================================================
CREATE TABLE warehouse.consumable_batches (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    item_id                 UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    batch_no                TEXT NOT NULL,
    total_qty               INTEGER NOT NULL CHECK (total_qty >= 0),
    remaining_qty           INTEGER NOT NULL CHECK (remaining_qty >= 0),
    expiry_date             DATE,
    received_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    supplier_id             UUID,
    current_warehouse_id    UUID REFERENCES warehouse.warehouses(id) ON DELETE SET NULL,
    unit_cost               NUMERIC(14, 4),
    status                  TEXT NOT NULL DEFAULT 'in_stock'
        CHECK (status IN ('in_stock','allocated','consumed','expired','disposed')),
    notes                   TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (item_id, batch_no),
    CHECK (remaining_qty <= total_qty)
);

CREATE INDEX idx_consumable_batches_item_status   ON warehouse.consumable_batches(item_id, status);
CREATE INDEX idx_consumable_batches_wh_status     ON warehouse.consumable_batches(current_warehouse_id, status);
CREATE INDEX idx_consumable_batches_expiry        ON warehouse.consumable_batches(expiry_date) WHERE expiry_date IS NOT NULL;
CREATE INDEX idx_consumable_batches_fifo          ON warehouse.consumable_batches(item_id, received_at) WHERE status = 'in_stock';

-- =====================================================================
-- 7. batch_consumption_log — per-WO consumption audit (immutable)
-- =====================================================================
CREATE TABLE warehouse.batch_consumption_log (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    consumable_batch_id      UUID NOT NULL REFERENCES warehouse.consumable_batches(id) ON DELETE RESTRICT,
    wo_id                    UUID,
    qty_consumed             INTEGER NOT NULL CHECK (qty_consumed > 0),
    consumed_by              UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    consumed_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notes                    TEXT
);

CREATE INDEX idx_batch_consumption_log_batch_time
    ON warehouse.batch_consumption_log(consumable_batch_id, consumed_at DESC);
CREATE INDEX idx_batch_consumption_log_wo
    ON warehouse.batch_consumption_log(wo_id) WHERE wo_id IS NOT NULL;

-- =====================================================================
-- 8. sub_warehouses — NOC + TL stockholder model
-- =====================================================================
--
-- A sub-warehouse is a child of a parent warehouse (typically Regional).
-- The owner_user_id is the TL / NOC supervisor who is liable for stock
-- variance + receives the threshold alert before it escalates upstream.
-- is_mobile=TRUE for technician vehicle stocks; vehicle_id is free-form
-- (could be a plate number, fleet id, etc.).
CREATE TABLE warehouse.sub_warehouses (
    id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    parent_warehouse_id   UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    name                  TEXT NOT NULL,
    code                  TEXT NOT NULL UNIQUE,
    owner_user_id         UUID NOT NULL REFERENCES identity.users(id) ON DELETE RESTRICT,
    owner_role            TEXT NOT NULL DEFAULT 'team_lead'
        CHECK (owner_role IN ('team_lead','noc_supervisor','technician','warehouse_staff')),
    is_mobile             BOOLEAN NOT NULL DEFAULT TRUE,
    vehicle_id            TEXT,
    can_purchase          BOOLEAN NOT NULL DEFAULT FALSE,
    active                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sub_warehouses_parent     ON warehouse.sub_warehouses(parent_warehouse_id);
CREATE INDEX idx_sub_warehouses_owner      ON warehouse.sub_warehouses(owner_user_id, active);

-- =====================================================================
-- 9. asset_location_history — per-asset movement audit (immutable)
-- =====================================================================
--
-- The denormalized assets.current_location_id mirrors the latest row
-- here. movement_kind names the operational verb; reason carries the
-- free-form context (e.g. "Customer terminated", "Retrofit donor",
-- "Lost in transit pending investigation").
CREATE TABLE warehouse.asset_location_history (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    asset_id                 UUID NOT NULL REFERENCES warehouse.assets(id) ON DELETE CASCADE,
    from_warehouse_id        UUID REFERENCES warehouse.warehouses(id) ON DELETE SET NULL,
    to_warehouse_id          UUID REFERENCES warehouse.warehouses(id) ON DELETE SET NULL,
    from_sub_warehouse_id    UUID REFERENCES warehouse.sub_warehouses(id) ON DELETE SET NULL,
    to_sub_warehouse_id      UUID REFERENCES warehouse.sub_warehouses(id) ON DELETE SET NULL,
    movement_kind            TEXT NOT NULL
        CHECK (movement_kind IN ('receive','transfer','dispatch','return','consume','retire','install','decommission','in_transit')),
    wo_id                    UUID,
    customer_id              UUID,
    moved_by                 UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    moved_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason                   TEXT,
    location_label           TEXT
);

CREATE INDEX idx_asset_location_history_asset ON warehouse.asset_location_history(asset_id, moved_at DESC);
CREATE INDEX idx_asset_location_history_wo    ON warehouse.asset_location_history(wo_id) WHERE wo_id IS NOT NULL;
CREATE INDEX idx_asset_location_history_time  ON warehouse.asset_location_history(moved_at DESC);

-- =====================================================================
-- 10. opname_tablet_sessions — offline-first tablet sync + reconcile
-- =====================================================================
--
-- Reuses the existing warehouse.opname_sessions header (Wave 0006/M3 r2).
-- One tablet session represents a single device's offline collection
-- run; the offline_payload_hash UNIQUE constraint provides idempotent
-- sync (a retry from a flaky network can't double-apply).
CREATE TABLE warehouse.opname_tablet_sessions (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    opname_session_id        UUID NOT NULL REFERENCES warehouse.opname_sessions(id) ON DELETE CASCADE,
    device_id                TEXT NOT NULL,
    technician_user_id       UUID NOT NULL REFERENCES identity.users(id) ON DELETE RESTRICT,
    started_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at             TIMESTAMPTZ,
    total_scans              INTEGER NOT NULL DEFAULT 0,
    sync_status              TEXT NOT NULL DEFAULT 'in_progress'
        CHECK (sync_status IN ('in_progress','synced','failed','reconciled')),
    offline_payload_hash     TEXT,
    last_synced_at           TIMESTAMPTZ,
    notes                    TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (opname_session_id, offline_payload_hash)
);

CREATE INDEX idx_opname_tablet_sessions_opname
    ON warehouse.opname_tablet_sessions(opname_session_id, sync_status);
CREATE INDEX idx_opname_tablet_sessions_tech
    ON warehouse.opname_tablet_sessions(technician_user_id, started_at DESC);

-- =====================================================================
-- 11. warehouses extension — can_purchase flag for Manual Purchase Entry
-- =====================================================================
--
-- Per the catalog TC-MPE-001..003: Regional default true, Area default
-- false (toggleable), Sub-WH hardcoded false (enforced at app layer,
-- migration just provides the column).
ALTER TABLE warehouse.warehouses
    ADD COLUMN IF NOT EXISTS can_purchase BOOLEAN NOT NULL DEFAULT FALSE;

-- Regional warehouses default true on first create. We don't back-fill;
-- ops sets the right value per warehouse via the admin endpoint.

-- =====================================================================
-- 12. Seed item categories — 12+ rows spanning the 4 types
-- =====================================================================
INSERT INTO warehouse.item_categories
    (id, code, name, type_code, description, default_unit,
     sub_warehouse_allowed_default, requires_serial_at_intake)
VALUES
    -- Type 1 — Serialized devices (one per unit, QR-tracked)
    ('00000000-0000-0000-0000-000000117001', 'ont',          'ONT',             'type1', 'Optical Network Terminal — customer-premise', 'pcs', TRUE, TRUE),
    ('00000000-0000-0000-0000-000000117002', 'router',       'Wi-Fi Router',    'type1', 'Customer-premise Wi-Fi router',              'pcs', TRUE, TRUE),
    ('00000000-0000-0000-0000-000000117003', 'switch',       'Managed Switch',  'type1', 'L2/L3 switch — enterprise CPE',              'pcs', TRUE, TRUE),
    ('00000000-0000-0000-0000-000000117004', 'mediaconv',    'Media Converter', 'type1', 'Fiber↔copper media converter',               'pcs', TRUE, TRUE),
    -- Type 2 — Cable (length-tracked)
    ('00000000-0000-0000-0000-000000117005', 'cable_fiber',  'Fiber Cable SM',  'type2', 'Single-mode fiber drum',                      'meters', FALSE, FALSE),
    ('00000000-0000-0000-0000-000000117006', 'cable_cat6',   'Cat6 Cable',      'type2', 'UTP Cat6 box/spool',                          'meters', FALSE, FALSE),
    ('00000000-0000-0000-0000-000000117007', 'cable_coax',   'Coaxial Cable',   'type2', 'RG-6 / RG-11 coaxial drum',                   'meters', FALSE, FALSE),
    -- Type 3 — Consumables (bulk count)
    ('00000000-0000-0000-0000-000000117008', 'connector_sc', 'SC Connector',    'type3', 'SC/APC fiber connector — pack',               'pcs', TRUE, FALSE),
    ('00000000-0000-0000-0000-000000117009', 'splice',       'Splice Sleeve',   'type3', 'Heat-shrink splice sleeve',                   'pcs', TRUE, FALSE),
    ('00000000-0000-0000-0000-000000117010', 'patchcord',    'Patch Cord',      'type3', 'Pre-terminated fiber patch cord',             'pcs', TRUE, FALSE),
    -- Type 4 — Network infrastructure (serialized + location-bound)
    ('00000000-0000-0000-0000-000000117011', 'olt',          'OLT',             'type4', 'Optical Line Terminal — POP',                 'pcs', FALSE, TRUE),
    ('00000000-0000-0000-0000-000000117012', 'odc',          'ODC',             'type4', 'Optical Distribution Cabinet',                'pcs', FALSE, TRUE),
    ('00000000-0000-0000-0000-000000117013', 'odp',          'ODP',             'type4', 'Optical Distribution Point',                  'pcs', FALSE, TRUE),
    ('00000000-0000-0000-0000-000000117014', 'splitter',     'Splitter',        'type4', 'PON splitter (1:8 / 1:16 / 1:32)',            'pcs', FALSE, TRUE)
ON CONFLICT (id) DO NOTHING;

-- =====================================================================
-- 13. Permissions — additive grants for the new surfaces
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('warehouse', 'item.category.read',      'View item categories'),
    ('warehouse', 'item.category.write',     'Create / edit item categories'),
    ('warehouse', 'serialized.scan',         'Scan serialized assets via QR'),
    ('warehouse', 'serialized.track',        'View serialized asset history'),
    ('warehouse', 'cable.cut',               'Cut a length from a cable lot'),
    ('warehouse', 'cable.track',             'View cable lots + cut history'),
    ('warehouse', 'consumable.consume',      'Consume from a consumable batch'),
    ('warehouse', 'consumable.track',        'View consumable batches + consumption log'),
    ('warehouse', 'sub_warehouse.read',      'View sub-warehouses'),
    ('warehouse', 'sub_warehouse.manage',    'Create / edit sub-warehouses'),
    ('warehouse', 'asset.location.read',     'View asset location history'),
    ('warehouse', 'opname.tablet.sync',      'Sync offline tablet opname payloads'),
    ('warehouse', 'qr.generate',             'Generate QR codes for items'),
    ('warehouse', 'qr.scan',                 'Scan a QR code and resolve item')
ON CONFLICT DO NOTHING;

-- super_admin → all new warehouse permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'warehouse'
  AND p.action IN (
      'item.category.read','item.category.write',
      'serialized.scan','serialized.track',
      'cable.cut','cable.track',
      'consumable.consume','consumable.track',
      'sub_warehouse.read','sub_warehouse.manage',
      'asset.location.read','opname.tablet.sync',
      'qr.generate','qr.scan'
  )
ON CONFLICT DO NOTHING;

-- warehouse_manager → everything except tablet sync (techs only)
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND p.module = 'warehouse'
  AND p.action IN (
      'item.category.read','item.category.write',
      'serialized.scan','serialized.track',
      'cable.cut','cable.track',
      'consumable.consume','consumable.track',
      'sub_warehouse.read','sub_warehouse.manage',
      'asset.location.read',
      'qr.generate','qr.scan'
  )
ON CONFLICT DO NOTHING;

-- technician → field-only subset: scan, cable cut, consumable consume,
-- asset location read, tablet sync. No category management.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'warehouse'
  AND p.action IN (
      'serialized.scan','serialized.track',
      'cable.cut','cable.track',
      'consumable.consume','consumable.track',
      'sub_warehouse.read',
      'asset.location.read',
      'opname.tablet.sync',
      'qr.scan'
  )
ON CONFLICT DO NOTHING;
