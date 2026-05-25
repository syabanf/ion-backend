-- Wave 133 down — remove demo seed rows inserted by the up migration.
-- Deletes are ordered to respect FK dependencies (children first).
-- Safe to run on any environment; rows identified by their stable
-- codes/numbers so non-demo data is never touched.

BEGIN;

-- Work orders
DELETE FROM field.work_orders
WHERE wo_number IN (
    'WO-DEMO-INSTALL-001',
    'WO-DEMO-INSTALL-002',
    'WO-DEMO-ENT-001'
);

-- Field teams
DELETE FROM field.teams
WHERE code IN ('TEAM-NORTH-01', 'TEAM-SOUTH-01');

-- Leads
DELETE FROM crm.leads
WHERE lead_number IN (
    'LEAD-BB-00001',
    'LEAD-BB-00002',
    'LEAD-ENT-00001'
);

-- Customers
DELETE FROM crm.customers
WHERE customer_number IN (
    'CUST-BB-00001',
    'CUST-BB-00002',
    'CUST-ENT-00001'
);

-- Stock levels (all rows for the demo warehouses)
DELETE FROM warehouse.stock_levels
WHERE warehouse_id IN (
    SELECT id FROM warehouse.warehouses
    WHERE code IN ('WH-MAIN', 'WH-FIELD-NORTH', 'WH-FIELD-SOUTH')
);

-- Stock items seeded by this migration
DELETE FROM warehouse.stock_items
WHERE sku IN (
    'ONT-HG8245H',
    'WIFI-RT-AC1200',
    'SWITCH-8P-UNIFI',
    'FIBER-SM-G652D',
    'CABLE-CAT6-UTP',
    'CONN-SC-APC',
    'SPLICE-SLEEVE-60',
    'SPLITTER-1X8-SCAPC'
);

-- Warehouses
DELETE FROM warehouse.warehouses
WHERE code IN ('WH-MAIN', 'WH-FIELD-NORTH', 'WH-FIELD-SOUTH');

-- Network nodes (ODPs first, then ODCs, then OLT)
DELETE FROM network.nodes
WHERE code IN (
    'ODP-KG-01', 'ODP-KG-02', 'ODP-SUNTER-01', 'ODP-KBY-01',
    'ODC-NORTH-01', 'ODC-SOUTH-01',
    'OLT-HQ-01'
);

-- Branches (sub_areas first, then areas — HQ left intact)
DELETE FROM identity.branches
WHERE code IN (
    'SA-KG', 'SA-SUNTER', 'SA-KEBAYORAN', 'SA-KEMANG',
    'AREA-JKT-UTARA', 'AREA-JKT-SELATAN'
);

COMMIT;
