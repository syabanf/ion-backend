-- Wave 113 — Network Device Lifecycle (Phase 1B Broadband).
--
-- Scope: complete the single largest uncovered Phase 1B module (32 TC-NDL-*
-- cases) by introducing a NEW bounded context that owns hardware lifecycle
-- end-to-end: receive → commission → operate → diagnose → swap/repair →
-- RMA → decommission.
--
-- Bounded-context rules (mirror of reseller / partnership / vendormgmt):
--   - Schema `netdev` is the sole namespace this service touches.
--   - Cross-context references (customer_id, warehouse_id, service_location_id,
--     wo_id, retrofit_id, fault_event_id) are stored as plain UUIDs — no FK
--     to other schemas. That keeps the future extraction into its own
--     binary (cmd/netdevices-svc, see Wave 113 #9) trivial and disjoint
--     from the parallel Wave 111 (payment) + Wave 112 (nocmon) waves.
--   - State machines live in the domain layer; the DB CHECK clauses below
--     are belt-and-suspenders so even a manual SQL hot-patch can't write
--     a bogus status.

CREATE SCHEMA IF NOT EXISTS netdev;

-- =====================================================================
-- devices — single row per physical unit
-- =====================================================================
--
-- status lifecycle (enforced in domain.Device + CHECK below):
--   in_stock → allocated → commissioned → active ↔ degraded
--   active|degraded → quarantine
--   any → rma_open → rma_returned
--   any-non-terminal → decommissioned   (terminal)
--
-- Identity is the serial number (vendor-provided). MAC + asset_tag are
-- optional secondary keys for cases where a SN sticker is unreadable.
CREATE TABLE netdev.devices (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    serial_no            TEXT NOT NULL UNIQUE,
    mac_addr             TEXT,
    asset_tag            TEXT,
    kind                 TEXT NOT NULL
        CHECK (kind IN ('ont','olt_port','router','switch','ap','onu','onx','mediaconverter','other')),
    model                TEXT,
    manufacturer         TEXT,
    firmware_version     TEXT,
    status               TEXT NOT NULL DEFAULT 'in_stock'
        CHECK (status IN (
            'in_stock','allocated','commissioned','active','degraded',
            'quarantine','rma_open','rma_returned','decommissioned'
        )),
    warehouse_id         UUID,
    customer_id          UUID,
    service_location_id  UUID,
    ip_address           TEXT,
    mgmt_uri             TEXT,
    last_seen_at         TIMESTAMPTZ,
    commissioned_at      TIMESTAMPTZ,
    decommissioned_at    TIMESTAMPTZ,
    notes                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_netdev_devices_status_kind  ON netdev.devices (status, kind);
CREATE INDEX idx_netdev_devices_customer     ON netdev.devices (customer_id, status);
CREATE INDEX idx_netdev_devices_mac          ON netdev.devices (mac_addr) WHERE mac_addr IS NOT NULL;
CREATE INDEX idx_netdev_devices_firmware     ON netdev.devices (firmware_version);

-- =====================================================================
-- firmware_versions — catalog of known firmware images per (kind, model)
-- =====================================================================
--
-- A row here doesn't grant any device a new firmware — it just lets the
-- compliance scanner know "for an `ont` of model `Huawei-HG8245H`, the
-- currently-recommended firmware is V100R019C10SPC202". is_critical
-- promotes a non-compliant device into the "critical_pending" bucket
-- in the compliance report so NOC sees it.
CREATE TABLE netdev.firmware_versions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    kind            TEXT NOT NULL,
    model           TEXT NOT NULL,
    version         TEXT NOT NULL,
    release_notes   TEXT,
    is_recommended  BOOLEAN NOT NULL DEFAULT FALSE,
    is_critical     BOOLEAN NOT NULL DEFAULT FALSE,
    released_at     DATE,
    supported_until DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (kind, model, version)
);

-- =====================================================================
-- firmware_upgrade_jobs — one row per scheduled upgrade attempt
-- =====================================================================
--
-- status lifecycle:
--   scheduled → staged → in_progress → succeeded
--                                   ↘ failed → rolled_back (after max_retries)
--   scheduled|staged → cancelled
CREATE TABLE netdev.firmware_upgrade_jobs (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    device_id            UUID NOT NULL REFERENCES netdev.devices(id) ON DELETE RESTRICT,
    target_firmware_id   UUID REFERENCES netdev.firmware_versions(id),
    scheduled_at         TIMESTAMPTZ,
    started_at           TIMESTAMPTZ,
    completed_at         TIMESTAMPTZ,
    status               TEXT NOT NULL DEFAULT 'scheduled'
        CHECK (status IN (
            'scheduled','staged','in_progress','succeeded','failed','rolled_back','cancelled'
        )),
    retry_count          INT NOT NULL DEFAULT 0,
    max_retries          INT NOT NULL DEFAULT 3,
    error_msg            TEXT,
    previous_firmware    TEXT,
    created_by           UUID,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_netdev_upgrade_status_sched ON netdev.firmware_upgrade_jobs (status, scheduled_at);
CREATE INDEX idx_netdev_upgrade_device_start ON netdev.firmware_upgrade_jobs (device_id, started_at DESC);

-- =====================================================================
-- device_swaps — workflow for replacing a faulty device in the field
-- =====================================================================
--
-- status lifecycle:
--   requested → approved → staged → technician_assigned → swapped → closed
--                                                      ↘ rolled_back (from swapped only)
--
-- The bridge to warehouse Asset Retrofit (Wave 87) is via retrofit_id —
-- when the swap completes, the orchestrator calls the WarehouseRetrofit
-- port (see internal/netdevices/port) which records the consume+produce
-- movement on the swapped assets.
CREATE TABLE netdev.device_swaps (
    id                     UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id            UUID NOT NULL,
    faulty_device_id       UUID NOT NULL REFERENCES netdev.devices(id),
    replacement_device_id  UUID REFERENCES netdev.devices(id),
    reason                 TEXT,
    fault_event_id         UUID,
    status                 TEXT NOT NULL DEFAULT 'requested'
        CHECK (status IN (
            'requested','approved','staged','technician_assigned',
            'swapped','rolled_back','closed'
        )),
    wo_id                  UUID,
    technician_user_id     UUID,
    swap_started_at        TIMESTAMPTZ,
    swap_completed_at      TIMESTAMPTZ,
    retrofit_id            UUID,
    requested_by           UUID,
    approved_by            UUID,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_netdev_swaps_status_created ON netdev.device_swaps (status, created_at DESC);
CREATE INDEX idx_netdev_swaps_customer       ON netdev.device_swaps (customer_id);
CREATE INDEX idx_netdev_swaps_faulty         ON netdev.device_swaps (faulty_device_id);

-- =====================================================================
-- rma_records — vendor return-material authorisations
-- =====================================================================
--
-- status lifecycle:
--   open → shipped → received → replaced → closed
--                            ↘ rejected → closed
--   open|shipped|received|replaced|rejected → expired (after 90d untouched,
--                                                      written by cron)
CREATE TABLE netdev.rma_records (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    device_id          UUID NOT NULL REFERENCES netdev.devices(id) ON DELETE RESTRICT,
    vendor             TEXT,
    vendor_rma_no      TEXT,
    return_reason      TEXT,
    shipped_at         TIMESTAMPTZ,
    received_at        TIMESTAMPTZ,
    replacement_serial TEXT,
    status             TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','shipped','received','replaced','rejected','closed','expired')),
    notes              TEXT,
    created_by         UUID,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_netdev_rma_status_created ON netdev.rma_records (status, created_at DESC);
CREATE INDEX idx_netdev_rma_device         ON netdev.rma_records (device_id);
CREATE INDEX idx_netdev_rma_vendor_rma_no  ON netdev.rma_records (vendor_rma_no) WHERE vendor_rma_no IS NOT NULL;

-- =====================================================================
-- device_health_snapshots — time-series of operational health
-- =====================================================================
--
-- Ingested by the NOC polling pipeline (later wave) or the device-mgmt
-- adapter directly. ComputeHealthScore() in domain/device_health.go
-- collapses these into a 0–100 score. raw_payload preserves the
-- vendor-specific blob (SNMP OID dump, OLT signal table, etc) for
-- diagnostic deep-dives.
CREATE TABLE netdev.device_health_snapshots (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    device_id       UUID NOT NULL REFERENCES netdev.devices(id) ON DELETE CASCADE,
    snapped_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uptime_seconds  BIGINT,
    signal_dbm      NUMERIC(6,2),
    packet_loss_pct NUMERIC(5,2),
    cpu_pct         NUMERIC(5,2),
    memory_pct      NUMERIC(5,2),
    raw_payload     JSONB
);

CREATE INDEX idx_netdev_health_device_snap ON netdev.device_health_snapshots (device_id, snapped_at DESC);

-- =====================================================================
-- firmware_compliance_runs — audit of compliance scans
-- =====================================================================
--
-- Cron writes one row per scan. report_payload is the per-device verdict
-- map (device_id → compliant|non_compliant|critical_pending). The
-- aggregate counters on the header power the dashboard tile so the FE
-- never has to crack the JSON for the headline numbers.
CREATE TABLE netdev.firmware_compliance_runs (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at       TIMESTAMPTZ,
    scope             TEXT NOT NULL DEFAULT 'all',
    total_devices     INT,
    compliant         INT,
    non_compliant     INT,
    critical_pending  INT,
    report_payload    JSONB
);

CREATE INDEX idx_netdev_compliance_started ON netdev.firmware_compliance_runs (started_at DESC);

-- =====================================================================
-- Permissions — netdev module catalog
-- =====================================================================
--
-- Permission grants for built-in roles seed the typical operating mix:
--   - super_admin / noc_admin: full power
--   - noc_engineer: everything EXCEPT decommission (joint approval gate)
--   - warehouse_manager: device read/write + swap read + full RMA
--   - technician: device read + swap execute + health read
INSERT INTO identity.permissions (module, action, description) VALUES
    ('netdev', 'device.read',         'View network devices'),
    ('netdev', 'device.write',        'Register / edit network devices'),
    ('netdev', 'device.commission',   'Commission a device to a customer'),
    ('netdev', 'device.decommission', 'Permanently retire a device'),
    ('netdev', 'firmware.read',       'View firmware catalog + upgrade jobs'),
    ('netdev', 'firmware.upgrade',    'Schedule / execute firmware upgrades'),
    ('netdev', 'swap.read',           'View device swaps'),
    ('netdev', 'swap.request',        'Request a device swap'),
    ('netdev', 'swap.approve',        'Approve a device swap'),
    ('netdev', 'swap.execute',        'Execute a device swap in the field'),
    ('netdev', 'rma.read',            'View RMA records'),
    ('netdev', 'rma.write',           'Open / update RMA records'),
    ('netdev', 'rma.close',           'Close RMA records'),
    ('netdev', 'health.read',         'View device health snapshots'),
    ('netdev', 'compliance.read',     'View firmware compliance reports')
ON CONFLICT DO NOTHING;

-- super_admin → all netdev permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin' AND p.module = 'netdev'
ON CONFLICT DO NOTHING;

-- noc_admin → all netdev permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc_admin' AND p.module = 'netdev'
ON CONFLICT DO NOTHING;

-- noc_engineer → everything except decommission
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc_engineer'
  AND p.module = 'netdev'
  AND p.action <> 'device.decommission'
ON CONFLICT DO NOTHING;

-- warehouse_manager → device read/write + swap read + full RMA
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'warehouse_manager'
  AND p.module = 'netdev'
  AND p.action IN ('device.read','device.write','swap.read','rma.read','rma.write','rma.close')
ON CONFLICT DO NOTHING;

-- technician → device read + swap execute + health read
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'netdev'
  AND p.action IN ('device.read','swap.execute','health.read')
ON CONFLICT DO NOTHING;

-- =====================================================================
-- Demo seed
-- =====================================================================
--
-- Three devices (one in_stock, one commissioned, one active router) and
-- two firmware versions give the FE a non-empty list-view on first
-- boot — same idempotent-fixed-uuid playbook as Wave 94.
INSERT INTO netdev.devices
    (id, serial_no, mac_addr, asset_tag, kind, model, manufacturer,
     firmware_version, status, warehouse_id, customer_id, service_location_id)
VALUES
    ('00000000-0000-0000-0000-000000011301',
     'ONT-DEMO-IN-STOCK-001', '00:11:22:33:44:55', 'AT-NDV-001',
     'ont', 'HG8245H', 'Huawei',
     'V100R019C10SPC100', 'in_stock',
     '00000000-0000-0000-0000-000000060001', NULL, NULL),
    ('00000000-0000-0000-0000-000000011302',
     'ONT-DEMO-COMM-002', '00:11:22:33:44:66', 'AT-NDV-002',
     'ont', 'HG8245H', 'Huawei',
     'V100R019C10SPC202', 'commissioned',
     '00000000-0000-0000-0000-000000060001',
     '00000000-0000-0000-0000-000000070001',
     '00000000-0000-0000-0000-000000070101'),
    ('00000000-0000-0000-0000-000000011303',
     'RT-DEMO-ACTIVE-003', 'aa:bb:cc:dd:ee:ff', 'AT-NDV-003',
     'router', 'RB4011', 'Mikrotik',
     'RouterOS-7.10.2', 'active',
     '00000000-0000-0000-0000-000000060001', NULL, NULL)
ON CONFLICT (id) DO NOTHING;

INSERT INTO netdev.firmware_versions
    (id, kind, model, version, release_notes, is_recommended, is_critical, released_at)
VALUES
    ('00000000-0000-0000-0000-000000011311',
     'ont', 'HG8245H', 'V100R019C10SPC202',
     'Security hardening + PON link stability fix.', TRUE, TRUE, '2026-01-15'),
    ('00000000-0000-0000-0000-000000011312',
     'router', 'RB4011', 'RouterOS-7.10.2',
     'Long-term stable release; kernel CVE patches.', TRUE, FALSE, '2026-03-01')
ON CONFLICT (kind, model, version) DO NOTHING;
