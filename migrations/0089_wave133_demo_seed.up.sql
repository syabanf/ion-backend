-- Wave 133 — Comprehensive demo data seed
--
-- Populates a fresh database with enough reference + demo data to
-- exercise every module through the dashboard, sales app, and tech app
-- without any manual data-entry after running migrations.
--
-- Sections:
--   1. Branches        — area (2) + sub_area (4) under HQ
--   2. Network nodes   — OLT → ODC → ODP hierarchy (7 nodes)
--   3. Warehouses      — Main (can_purchase) + Field warehouse
--   4. Stock items     — 8 SKUs (device, cable, consumable, infra)
--   5. Stock levels    — initial quantities per warehouse
--   6. Customers       — 2 broadband + 1 enterprise
--   7. Leads           — 3 leads (new / qualified / potential)
--   8. Field teams     — 2 teams (team_leader linked post seed-demo)
--   9. Work orders     — 3 demo WOs (BB completed, BB assigned, ENT dispatched)
--
-- All INSERTs are idempotent: ON CONFLICT … DO NOTHING.
-- FKs are resolved by subquery — no hardcoded UUIDs.
-- User-dependent FKs (sales_id, team_leader_id, created_by)
-- default to NULL; they are wired by cmd/seed-demo at runtime.
--
-- Down: see 0089_wave133_demo_seed.down.sql

BEGIN;

-- ──────────────────────────────────────────────────────────────────────
-- 1. BRANCHES
-- ──────────────────────────────────────────────────────────────────────

-- Area: Jakarta Utara (under HQ)
INSERT INTO identity.branches (name, code, level, parent_id, active)
SELECT 'Area Jakarta Utara', 'AREA-JKT-UTARA', 'area', b.id, TRUE
FROM identity.branches b WHERE b.code = 'HQ'
ON CONFLICT (code) DO NOTHING;

-- Area: Jakarta Selatan (under HQ)
INSERT INTO identity.branches (name, code, level, parent_id, active)
SELECT 'Area Jakarta Selatan', 'AREA-JKT-SELATAN', 'area', b.id, TRUE
FROM identity.branches b WHERE b.code = 'HQ'
ON CONFLICT (code) DO NOTHING;

-- Sub-areas under Jakarta Utara
INSERT INTO identity.branches (name, code, level, parent_id, active)
SELECT 'Sub Area Kelapa Gading', 'SA-KG', 'sub_area', b.id, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-UTARA'
ON CONFLICT (code) DO NOTHING;

INSERT INTO identity.branches (name, code, level, parent_id, active)
SELECT 'Sub Area Sunter', 'SA-SUNTER', 'sub_area', b.id, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-UTARA'
ON CONFLICT (code) DO NOTHING;

-- Sub-areas under Jakarta Selatan
INSERT INTO identity.branches (name, code, level, parent_id, active)
SELECT 'Sub Area Kebayoran Baru', 'SA-KEBAYORAN', 'sub_area', b.id, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-SELATAN'
ON CONFLICT (code) DO NOTHING;

INSERT INTO identity.branches (name, code, level, parent_id, active)
SELECT 'Sub Area Kemang', 'SA-KEMANG', 'sub_area', b.id, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-SELATAN'
ON CONFLICT (code) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 2. NETWORK TOPOLOGY
--    OLT-HQ-01  (regional — under HQ)
--      └── ODC-NORTH-01  (area — Utara)
--      │     └── ODP-KG-01      (sub_area — Kelapa Gading)
--      │     └── ODP-KG-02      (sub_area — Kelapa Gading)
--      │     └── ODP-SUNTER-01  (sub_area — Sunter)
--      └── ODC-SOUTH-01  (area — Selatan)
--            └── ODP-KBY-01     (sub_area — Kebayoran Baru)
-- ──────────────────────────────────────────────────────────────────────

-- OLT (parent = NULL, regional anchor)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id, total_ports, status, active)
SELECT
    nt.id,
    'OLT Jakarta HQ', 'OLT-HQ-01',
    b.id, NULL, 16, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches b ON b.code = 'HQ'
WHERE nt.type_key = 'olt'
ON CONFLICT (code) DO NOTHING;

-- ODC North (parent = OLT-HQ-01)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id, total_ports, status, active)
SELECT
    nt.id,
    'ODC Jakarta Utara', 'ODC-NORTH-01',
    b.id, parent.id, 8, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches   b      ON b.code     = 'AREA-JKT-UTARA'
JOIN network.nodes       parent ON parent.code = 'OLT-HQ-01'
WHERE nt.type_key = 'odc'
ON CONFLICT (code) DO NOTHING;

-- ODC South (parent = OLT-HQ-01)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id, total_ports, status, active)
SELECT
    nt.id,
    'ODC Jakarta Selatan', 'ODC-SOUTH-01',
    b.id, parent.id, 8, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches   b      ON b.code     = 'AREA-JKT-SELATAN'
JOIN network.nodes       parent ON parent.code = 'OLT-HQ-01'
WHERE nt.type_key = 'odc'
ON CONFLICT (code) DO NOTHING;

-- ODP Kelapa Gading 1 (parent = ODC-NORTH-01)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id,
     coverage_radius_m, total_ports, status, active)
SELECT
    nt.id,
    'ODP Kelapa Gading 1', 'ODP-KG-01',
    b.id, parent.id,
    500, 4, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches   b      ON b.code     = 'SA-KG'
JOIN network.nodes       parent ON parent.code = 'ODC-NORTH-01'
WHERE nt.type_key = 'odp'
ON CONFLICT (code) DO NOTHING;

-- ODP Kelapa Gading 2 (parent = ODC-NORTH-01)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id,
     coverage_radius_m, total_ports, status, active)
SELECT
    nt.id,
    'ODP Kelapa Gading 2', 'ODP-KG-02',
    b.id, parent.id,
    500, 4, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches   b      ON b.code     = 'SA-KG'
JOIN network.nodes       parent ON parent.code = 'ODC-NORTH-01'
WHERE nt.type_key = 'odp'
ON CONFLICT (code) DO NOTHING;

-- ODP Sunter 1 (parent = ODC-NORTH-01)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id,
     coverage_radius_m, total_ports, status, active)
SELECT
    nt.id,
    'ODP Sunter 1', 'ODP-SUNTER-01',
    b.id, parent.id,
    500, 4, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches   b      ON b.code     = 'SA-SUNTER'
JOIN network.nodes       parent ON parent.code = 'ODC-NORTH-01'
WHERE nt.type_key = 'odp'
ON CONFLICT (code) DO NOTHING;

-- ODP Kebayoran Baru 1 (parent = ODC-SOUTH-01)
INSERT INTO network.nodes
    (node_type_id, name, code, branch_id, parent_id,
     coverage_radius_m, total_ports, status, active)
SELECT
    nt.id,
    'ODP Kebayoran Baru 1', 'ODP-KBY-01',
    b.id, parent.id,
    500, 4, 'active', TRUE
FROM network.node_types nt
JOIN identity.branches   b      ON b.code     = 'SA-KEBAYORAN'
JOIN network.nodes       parent ON parent.code = 'ODC-SOUTH-01'
WHERE nt.type_key = 'odp'
ON CONFLICT (code) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 3. WAREHOUSES
-- ──────────────────────────────────────────────────────────────────────

-- Main warehouse at HQ (purchasing-enabled, holds primary stock)
INSERT INTO warehouse.warehouses
    (name, code, branch_id, address, can_purchase, active)
SELECT
    'Gudang Utama HQ', 'WH-MAIN',
    b.id,
    'Gedung ION Network Lt. B1, Jakarta Pusat',
    TRUE, TRUE
FROM identity.branches b WHERE b.code = 'HQ'
ON CONFLICT (code) DO NOTHING;

-- Field warehouse Jakarta Utara (dispatch point for technicians)
INSERT INTO warehouse.warehouses
    (name, code, branch_id, address, can_purchase, active)
SELECT
    'Gudang Field Jakarta Utara', 'WH-FIELD-NORTH',
    b.id,
    'Jl. Kelapa Gading Raya, Jakarta Utara',
    FALSE, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-UTARA'
ON CONFLICT (code) DO NOTHING;

-- Field warehouse Jakarta Selatan
INSERT INTO warehouse.warehouses
    (name, code, branch_id, address, can_purchase, active)
SELECT
    'Gudang Field Jakarta Selatan', 'WH-FIELD-SOUTH',
    b.id,
    'Jl. Kebayoran Lama, Jakarta Selatan',
    FALSE, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-SELATAN'
ON CONFLICT (code) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 4. STOCK ITEMS
--    Type 1 — serialized_device (ONT, WiFi router, switch)
--    Type 2 — cable  (fiber, Cat6)
--    Type 3 — consumable (connectors, splice sleeves)
--    Type 4 — infrastructure (splitter)
-- ──────────────────────────────────────────────────────────────────────
INSERT INTO warehouse.stock_items
    (sku, name, category, brand, model, unit,
     serialized, item_type, tracking_kind, default_unit_cost, active)
VALUES
    -- Serialized devices
    ('ONT-HG8245H',
     'ONT Huawei HG8245H', 'serialized_device',
     'Huawei', 'HG8245H', 'pcs',
     TRUE, 'type1', 'serialized', 450000, TRUE),

    ('WIFI-RT-AC1200',
     'Wi-Fi Router TP-Link Archer C6', 'serialized_device',
     'TP-Link', 'Archer C6', 'pcs',
     TRUE, 'type1', 'serialized', 280000, TRUE),

    ('SWITCH-8P-UNIFI',
     'Switch 8-Port Ubiquiti UniFi USW-8', 'serialized_device',
     'Ubiquiti', 'USW-8', 'pcs',
     TRUE, 'type1', 'serialized', 1200000, TRUE),

    -- Cable (length-tracked)
    ('FIBER-SM-G652D',
     'Kabel Fiber Optik SM G.652D Drop', 'cable',
     'Corning', 'SMF-G652D', 'meters',
     FALSE, 'type2', 'length', 8500, TRUE),

    ('CABLE-CAT6-UTP',
     'Kabel LAN Cat6 UTP 305m Box', 'cable',
     'Belden', 'Cat6 UTP', 'meters',
     FALSE, 'type2', 'length', 4500, TRUE),

    -- Consumables (bulk quantity)
    ('CONN-SC-APC',
     'Konektor SC/APC Single Mode', 'consumable',
     NULL, NULL, 'pcs',
     FALSE, 'type3', 'bulk_quantity', 8500, TRUE),

    ('SPLICE-SLEEVE-60',
     'Splice Protection Sleeve 60 mm', 'consumable',
     NULL, NULL, 'pack',
     FALSE, 'type3', 'bulk_quantity', 25000, TRUE),

    -- Infrastructure (serialized)
    ('SPLITTER-1X8-SCAPC',
     'Optical Splitter PLC 1×8 SC/APC', 'infrastructure',
     'Fiberhome', '1×8 PLC Cassette', 'pcs',
     TRUE, 'type4', 'serialized', 185000, TRUE)

ON CONFLICT (sku) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 5. STOCK LEVELS — initial quantities per warehouse
-- ──────────────────────────────────────────────────────────────────────

-- Main warehouse (WH-MAIN) — full stock
INSERT INTO warehouse.stock_levels
    (warehouse_id, stock_item_id, quantity, min_threshold)
SELECT
    wh.id,
    si.id,
    v.qty,
    v.min_t
FROM (VALUES
    ('ONT-HG8245H',        50::NUMERIC,  10::NUMERIC),
    ('WIFI-RT-AC1200',     30::NUMERIC,   5::NUMERIC),
    ('SWITCH-8P-UNIFI',    15::NUMERIC,   3::NUMERIC),
    ('FIBER-SM-G652D',   2000::NUMERIC, 500::NUMERIC),
    ('CABLE-CAT6-UTP',   1000::NUMERIC, 200::NUMERIC),
    ('CONN-SC-APC',       500::NUMERIC, 100::NUMERIC),
    ('SPLICE-SLEEVE-60',  200::NUMERIC,  50::NUMERIC),
    ('SPLITTER-1X8-SCAPC', 20::NUMERIC,   5::NUMERIC)
) AS v(sku, qty, min_t)
JOIN warehouse.stock_items si ON si.sku = v.sku
JOIN warehouse.warehouses  wh ON wh.code = 'WH-MAIN'
ON CONFLICT (warehouse_id, stock_item_id) DO NOTHING;

-- Field warehouse North (WH-FIELD-NORTH) — working stock
INSERT INTO warehouse.stock_levels
    (warehouse_id, stock_item_id, quantity, min_threshold)
SELECT
    wh.id,
    si.id,
    v.qty,
    v.min_t
FROM (VALUES
    ('ONT-HG8245H',       10::NUMERIC,  2::NUMERIC),
    ('WIFI-RT-AC1200',     8::NUMERIC,  2::NUMERIC),
    ('FIBER-SM-G652D',   500::NUMERIC, 100::NUMERIC),
    ('CONN-SC-APC',      100::NUMERIC,  20::NUMERIC),
    ('SPLICE-SLEEVE-60',  50::NUMERIC,  10::NUMERIC)
) AS v(sku, qty, min_t)
JOIN warehouse.stock_items si ON si.sku = v.sku
JOIN warehouse.warehouses  wh ON wh.code = 'WH-FIELD-NORTH'
ON CONFLICT (warehouse_id, stock_item_id) DO NOTHING;

-- Field warehouse South (WH-FIELD-SOUTH) — working stock
INSERT INTO warehouse.stock_levels
    (warehouse_id, stock_item_id, quantity, min_threshold)
SELECT
    wh.id,
    si.id,
    v.qty,
    v.min_t
FROM (VALUES
    ('ONT-HG8245H',       8::NUMERIC,  2::NUMERIC),
    ('WIFI-RT-AC1200',    6::NUMERIC,  2::NUMERIC),
    ('FIBER-SM-G652D',  400::NUMERIC, 100::NUMERIC),
    ('CONN-SC-APC',      80::NUMERIC,  20::NUMERIC),
    ('SPLICE-SLEEVE-60', 40::NUMERIC,  10::NUMERIC)
) AS v(sku, qty, min_t)
JOIN warehouse.stock_items si ON si.sku = v.sku
JOIN warehouse.warehouses  wh ON wh.code = 'WH-FIELD-SOUTH'
ON CONFLICT (warehouse_id, stock_item_id) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 6. DEMO CUSTOMERS
--    CUST-BB-00001  Budi Santoso       broadband  active        (SA-KG)
--    CUST-BB-00002  Dewi Rahayu        broadband  pending_inst  (SA-SUNTER)
--    CUST-ENT-00001 PT Maju Bersama    enterprise active        (SA-KEBAYORAN)
-- ──────────────────────────────────────────────────────────────────────
INSERT INTO crm.customers
    (customer_number, customer_type, full_name, phone, email,
     address, branch_id, installation_node_id, status)
SELECT
    'CUST-BB-00001', 'broadband',
    'Budi Santoso', '08111000001', 'budi.santoso@demo.ion.local',
    'Jl. Kelapa Gading Raya No. 12, Jakarta Utara',
    b.id,
    n.id,   -- ODP-KG-01
    'active'
FROM identity.branches b
JOIN network.nodes n ON n.code = 'ODP-KG-01'
WHERE b.code = 'SA-KG'
ON CONFLICT (customer_number) DO NOTHING;

INSERT INTO crm.customers
    (customer_number, customer_type, full_name, phone, email,
     address, branch_id, installation_node_id, status)
SELECT
    'CUST-BB-00002', 'broadband',
    'Dewi Rahayu', '08111000002', 'dewi.rahayu@demo.ion.local',
    'Komp. Sunter Mas Blok C No. 5, Jakarta Utara',
    b.id,
    n.id,   -- ODP-SUNTER-01
    'pending_install'
FROM identity.branches b
JOIN network.nodes n ON n.code = 'ODP-SUNTER-01'
WHERE b.code = 'SA-SUNTER'
ON CONFLICT (customer_number) DO NOTHING;

INSERT INTO crm.customers
    (customer_number, customer_type, full_name, phone, email,
     address, branch_id, installation_node_id, status)
SELECT
    'CUST-ENT-00001', 'enterprise',
    'PT Maju Bersama Tbk', '02112345678', 'noc@majubersama.demo.ion.local',
    'Jl. Kebayoran Baru No. 88, Jakarta Selatan',
    b.id,
    n.id,   -- ODP-KBY-01
    'active'
FROM identity.branches b
JOIN network.nodes n ON n.code = 'ODP-KBY-01'
WHERE b.code = 'SA-KEBAYORAN'
ON CONFLICT (customer_number) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 7. DEMO LEADS
--    LEAD-BB-00001  Ahmad Fauzi           new        manual      (SA-KG)
--    LEAD-BB-00002  Siti Nurhaliza        qualified  sales_app   (SA-SUNTER)
--    LEAD-ENT-00001 CV Teknologi Nusantara potential  manual      (SA-KEBAYORAN)
-- ──────────────────────────────────────────────────────────────────────
INSERT INTO crm.leads
    (lead_number, status, full_name, phone, email,
     address, branch_id, source)
SELECT
    'LEAD-BB-00001', 'new',
    'Ahmad Fauzi', '08122000001', 'ahmad.fauzi@demo.ion.local',
    'Jl. Kelapa Gading Permai Blok A No. 3, Jakarta Utara',
    b.id, 'manual'
FROM identity.branches b WHERE b.code = 'SA-KG'
ON CONFLICT (lead_number) DO NOTHING;

INSERT INTO crm.leads
    (lead_number, status, full_name, phone, email,
     address, branch_id, source)
SELECT
    'LEAD-BB-00002', 'qualified',
    'Siti Nurhaliza', '08122000002', 'siti.nurhaliza@demo.ion.local',
    'Komp. Sunter Agung Blok D12, Jakarta Utara',
    b.id, 'sales_app'
FROM identity.branches b WHERE b.code = 'SA-SUNTER'
ON CONFLICT (lead_number) DO NOTHING;

INSERT INTO crm.leads
    (lead_number, status, full_name, phone, email,
     address, branch_id, source)
SELECT
    'LEAD-ENT-00001', 'potential',
    'CV Teknologi Nusantara', '02198765432', 'info@teknologi-nusantara.demo.ion.local',
    'Gedung Graha Nusantara Lt. 5, Kebayoran Baru, Jakarta Selatan',
    b.id, 'manual'
FROM identity.branches b WHERE b.code = 'SA-KEBAYORAN'
ON CONFLICT (lead_number) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 8. FIELD TEAMS
--    team_leader_id is NULL — wire it after cmd/seed-demo runs and
--    creates tl@ion.local / tech@ion.local users.
-- ──────────────────────────────────────────────────────────────────────
INSERT INTO field.teams
    (code, name, branch_id, team_leader_id, active)
SELECT
    'TEAM-NORTH-01', 'Tim Lapangan Jakarta Utara 1',
    b.id, NULL, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-UTARA'
ON CONFLICT (code) DO NOTHING;

INSERT INTO field.teams
    (code, name, branch_id, team_leader_id, active)
SELECT
    'TEAM-SOUTH-01', 'Tim Lapangan Jakarta Selatan 1',
    b.id, NULL, TRUE
FROM identity.branches b WHERE b.code = 'AREA-JKT-SELATAN'
ON CONFLICT (code) DO NOTHING;

-- ──────────────────────────────────────────────────────────────────────
-- 9. DEMO WORK ORDERS
--    WO-DEMO-INSTALL-001  BB  completed   CUST-BB-00001  (SA-KG)
--    WO-DEMO-INSTALL-002  BB  assigned    CUST-BB-00002  (SA-SUNTER)
--    WO-DEMO-ENT-001      ENT dispatched  CUST-ENT-00001 (SA-KEBAYORAN)
--
--    wo_category column added by migration 0088.
-- ──────────────────────────────────────────────────────────────────────

-- Completed broadband installation (CUST-BB-00001)
INSERT INTO field.work_orders
    (wo_number, customer_id, wo_type, product_type, wo_category,
     address, branch_id, priority, status, team_id)
SELECT
    'WO-DEMO-INSTALL-001',
    c.id,
    'new_installation', 'broadband', 'broadband',
    'Jl. Kelapa Gading Raya No. 12, Jakarta Utara',
    b.id, 'medium', 'completed',
    t.id
FROM crm.customers     c
JOIN identity.branches b ON b.code = 'SA-KG'
JOIN field.teams       t ON t.code = 'TEAM-NORTH-01'
WHERE c.customer_number = 'CUST-BB-00001'
ON CONFLICT (wo_number) DO NOTHING;

-- Assigned broadband installation (CUST-BB-00002) — tech can exercise pickup gate
INSERT INTO field.work_orders
    (wo_number, customer_id, wo_type, product_type, wo_category,
     address, branch_id, priority, status, team_id)
SELECT
    'WO-DEMO-INSTALL-002',
    c.id,
    'new_installation', 'broadband', 'broadband',
    'Komp. Sunter Mas Blok C No. 5, Jakarta Utara',
    b.id, 'medium', 'assigned',
    t.id
FROM crm.customers     c
JOIN identity.branches b ON b.code = 'SA-SUNTER'
JOIN field.teams       t ON t.code = 'TEAM-NORTH-01'
WHERE c.customer_number = 'CUST-BB-00002'
ON CONFLICT (wo_number) DO NOTHING;

-- Dispatched enterprise installation (CUST-ENT-00001) — shows ENT badge
INSERT INTO field.work_orders
    (wo_number, customer_id, wo_type, product_type, wo_category,
     address, branch_id, priority, status, team_id)
SELECT
    'WO-DEMO-ENT-001',
    c.id,
    'new_installation', 'broadband', 'enterprise',
    'Gedung Graha Nusantara Lt. 5, Kebayoran Baru, Jakarta Selatan',
    b.id, 'high', 'dispatched',
    t.id
FROM crm.customers     c
JOIN identity.branches b ON b.code = 'SA-KEBAYORAN'
JOIN field.teams       t ON t.code = 'TEAM-SOUTH-01'
WHERE c.customer_number = 'CUST-ENT-00001'
ON CONFLICT (wo_number) DO NOTHING;

COMMIT;
