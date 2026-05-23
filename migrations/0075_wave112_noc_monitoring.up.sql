-- Wave 112 — NOC monitoring bounded context.
--
-- Scope (PRD §Network §9.2 Service Monitoring, §9.3 Attenuation, §5.5
-- Fault Impact Analysis, §5.6 Topology Views): introduces a new
-- bounded context (schema `nocmon`) that owns:
--
--   * service_probes              — per-customer probe definitions
--   * service_health_samples      — time-series ingestion (partitioned)
--   * fiber_links                 — optical signal per ONU/OLT port
--   * fiber_attenuation_history   — append-only Rx-power history
--   * fault_events                — incident header (state machine)
--   * fault_impact_links          — per-customer impact join
--   * topology_snapshots          — cached topology blobs per scope
--
-- Cross-context contract:
--   - nocmon does NOT FK into network.* or crm.* — customer_id,
--     olt_port_id, etc. are plain UUIDs resolved at display time by
--     the calling service. Keeps the bounded context extractable.
--   - WO creation from a fault (TC-NAW-***) goes through an in-process
--     port (WorkOrderCreator) wired in cmd/nocmon-svc/main.go. No
--     direct SQL touches field.work_orders from this schema.
--
-- Lifecycle summary:
--   service_probes        : is_active=true ↔ false (deactivate via API)
--   fiber_links.status    : ok ↔ warn → critical → offline (or unknown)
--   fault_events.status   : open → acknowledged → investigating →
--                           mitigated → resolved (terminal)
--                         : open → duplicate (terminal)
--   topology_snapshots    : append-only; readers query the latest per
--                           (scope, scope_id)
--
-- Partitioning note: service_health_samples is PARTITIONED BY RANGE
-- (sampled_at). This wave creates two months of partitions; a follow-
-- up wave (113+) will ship the rolling-window cron.

BEGIN;

-- =====================================================================
-- Schema
-- =====================================================================
CREATE SCHEMA IF NOT EXISTS nocmon;

-- =====================================================================
-- nocmon.service_probes — per-customer probe definitions
-- =====================================================================
--
-- One row per (customer, probe_kind). interval_seconds controls how
-- often the cron tick runs the probe; threshold_warn/critical are
-- compared by domain.ServiceProbe.Evaluate(value). last_probed_at +
-- last_status are denormalized for the dashboard "unhealthy now"
-- query — the source of truth lives in service_health_samples.
CREATE TABLE nocmon.service_probes (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id         UUID NOT NULL,
    plan_id             UUID,
    probe_kind          TEXT NOT NULL
        CHECK (probe_kind IN ('rtt','packet_loss','throughput','speedtest','olt_signal')),
    probe_target        TEXT,
    interval_seconds    INTEGER NOT NULL DEFAULT 60,
    threshold_warn      NUMERIC(8,3),
    threshold_critical  NUMERIC(8,3),
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,
    last_probed_at      TIMESTAMPTZ,
    last_status         TEXT NOT NULL DEFAULT 'unknown',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_nocmon_probes_customer_kind
    ON nocmon.service_probes (customer_id, probe_kind);
CREATE INDEX idx_nocmon_probes_active_due
    ON nocmon.service_probes (is_active, last_probed_at);

-- =====================================================================
-- nocmon.service_health_samples — partitioned time-series
-- =====================================================================
--
-- Append-only ingestion. PARTITION BY RANGE (sampled_at) — the cron
-- runner pushes ~1 row per probe per minute; at 1k active probes the
-- table grows ~43M rows/month and partition pruning keeps reads cheap.
--
-- Idempotency: the (probe_id, sampled_at) unique index lets the cron
-- runner re-emit a sample without duplicating. We carry it on the
-- partitioned parent (forced to include the partition key) so every
-- child inherits the guard.
CREATE TABLE nocmon.service_health_samples (
    id          UUID NOT NULL DEFAULT uuid_generate_v4(),
    probe_id    UUID NOT NULL REFERENCES nocmon.service_probes(id) ON DELETE CASCADE,
    sampled_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    value       NUMERIC(12,4),
    status      TEXT NOT NULL DEFAULT 'ok'
        CHECK (status IN ('ok','warn','critical','unreachable')),
    PRIMARY KEY (id, sampled_at),
    UNIQUE (probe_id, sampled_at)
) PARTITION BY RANGE (sampled_at);

CREATE INDEX idx_nocmon_samples_probe_time
    ON nocmon.service_health_samples (probe_id, sampled_at DESC);

-- Initial partitions: current month + next month. The rolling-window
-- cron (future wave) will create N+2 month ahead on each tick.
DO $partitions$
DECLARE
    cur_start  DATE := date_trunc('month', NOW())::DATE;
    next_start DATE := (date_trunc('month', NOW()) + INTERVAL '1 month')::DATE;
    far_start  DATE := (date_trunc('month', NOW()) + INTERVAL '2 month')::DATE;
    cur_name   TEXT := 'service_health_samples_' || to_char(cur_start, 'YYYYMM');
    next_name  TEXT := 'service_health_samples_' || to_char(next_start, 'YYYYMM');
BEGIN
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS nocmon.%I PARTITION OF nocmon.service_health_samples FOR VALUES FROM (%L) TO (%L)',
        cur_name, cur_start, next_start
    );
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS nocmon.%I PARTITION OF nocmon.service_health_samples FOR VALUES FROM (%L) TO (%L)',
        next_name, next_start, far_start
    );
END
$partitions$;

-- =====================================================================
-- nocmon.fiber_links — optical signal per ONU/OLT port
-- =====================================================================
--
-- One row per (olt_port_id, onu_serial). Thresholds default to the
-- GPON spec (-25 dBm warn / -28 dBm critical from PRD §9.3); the
-- NOC Manager can adjust per OLT type / fiber spec at runtime by
-- updating the row.
--
-- Sign convention: optical Rx power readings are negative dBm, BUT
-- the domain stores the absolute loss magnitude (a positive number)
-- so "higher = worse" matches everywhere else in the system. The
-- adapter at the SNMP poller boundary is responsible for flipping
-- the sign. This keeps the threshold comparison straightforward:
-- warn when measured_db > warn_threshold_db; critical when measured_db
-- > critical_threshold_db.
CREATE TABLE nocmon.fiber_links (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    olt_port_id             UUID,
    onu_serial              TEXT,
    expected_loss_db        NUMERIC(5,2),
    warn_threshold_db       NUMERIC(5,2) NOT NULL DEFAULT 25.0,
    critical_threshold_db   NUMERIC(5,2) NOT NULL DEFAULT 28.0,
    last_measured_db        NUMERIC(5,2),
    last_measured_at        TIMESTAMPTZ,
    status                  TEXT NOT NULL DEFAULT 'unknown'
        CHECK (status IN ('ok','warn','critical','offline','unknown')),
    customer_id             UUID,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_nocmon_fiber_status_time
    ON nocmon.fiber_links (status, last_measured_at);
CREATE INDEX idx_nocmon_fiber_customer
    ON nocmon.fiber_links (customer_id);
CREATE INDEX idx_nocmon_fiber_onu_serial
    ON nocmon.fiber_links (onu_serial);

-- =====================================================================
-- nocmon.fiber_attenuation_history — append-only Rx-power history
-- =====================================================================
--
-- TC-FAM-003: per-port signal history for the 7/30/90 day trend
-- chart. Source defaults to 'snmp_poll' but the manual-entry path
-- (POST .../attenuation) stamps 'manual' so the trend chart can
-- distinguish operator-entered samples from polled ones.
CREATE TABLE nocmon.fiber_attenuation_history (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    fiber_link_id   UUID NOT NULL REFERENCES nocmon.fiber_links(id) ON DELETE CASCADE,
    measured_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    value_db        NUMERIC(5,2),
    source          TEXT NOT NULL DEFAULT 'snmp_poll'
);

CREATE INDEX idx_nocmon_fiber_history_link_time
    ON nocmon.fiber_attenuation_history (fiber_link_id, measured_at DESC);

-- =====================================================================
-- nocmon.fault_events — incident header
-- =====================================================================
--
-- Source linkage: (source_id, source_kind) is a polymorphic pointer
-- to whatever upstream signal opened the fault — a probe (kind='probe'),
-- a fiber link (kind='fiber'), an SNMP device-down (kind='device'),
-- or NULL for a NOC-entered manual outage. We deliberately don't FK
-- this to anything so the cross-context decoupling holds.
--
-- State machine (enforced in domain/fault_event.go):
--   open → acknowledged → investigating → mitigated → resolved (terminal)
--   open → duplicate (terminal; not part of the main flow)
--
-- customer_impact_count is a denormalized cache populated by
-- LinkImpact() so the dashboard list view doesn't need a JOIN+COUNT.
--
-- ticket_wo_id is the WO created via TC-NAW-001's
-- "Create Maintenance WO" button. The WorkOrderCreator port (in-
-- process today, HTTP later) returns the WO id which we stamp here.
CREATE TABLE nocmon.fault_events (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    kind                    TEXT NOT NULL
        CHECK (kind IN ('device_down','fiber_degradation','probe_critical','noc_alert','olt_port_flap','manual_outage')),
    severity                TEXT NOT NULL
        CHECK (severity IN ('low','medium','high','critical')),
    source_id               UUID,
    source_kind             TEXT,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    detected_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acknowledged_at         TIMESTAMPTZ,
    acknowledged_by         UUID,
    resolved_at             TIMESTAMPTZ,
    resolved_by             UUID,
    root_cause              TEXT,
    customer_impact_count   INTEGER NOT NULL DEFAULT 0,
    status                  TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','acknowledged','investigating','mitigated','resolved','duplicate')),
    ticket_wo_id            UUID,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_nocmon_faults_status_sev_time
    ON nocmon.fault_events (status, severity, started_at DESC);
CREATE INDEX idx_nocmon_faults_kind_time
    ON nocmon.fault_events (kind, started_at DESC);

-- =====================================================================
-- nocmon.fault_impact_links — per-customer impact join
-- =====================================================================
--
-- TC-FIA-002: the affected-customer list per fault. UNIQUE
-- (fault_event_id, customer_id) so re-running the cascade traversal
-- is idempotent; sla_credit_eligible is computed by
-- domain.ComputeSLACreditEligible at link-creation time and stamped
-- here so the SLA report doesn't need to recompute.
CREATE TABLE nocmon.fault_impact_links (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    fault_event_id          UUID NOT NULL REFERENCES nocmon.fault_events(id) ON DELETE CASCADE,
    customer_id             UUID NOT NULL,
    impact_kind             TEXT NOT NULL
        CHECK (impact_kind IN ('full_outage','degraded','intermittent','unknown')),
    impact_start            TIMESTAMPTZ,
    impact_end              TIMESTAMPTZ,
    sla_credit_eligible     BOOLEAN NOT NULL DEFAULT FALSE,
    notified_at             TIMESTAMPTZ,
    UNIQUE (fault_event_id, customer_id)
);

CREATE INDEX idx_nocmon_impact_customer_time
    ON nocmon.fault_impact_links (customer_id, impact_start DESC);
CREATE INDEX idx_nocmon_impact_sla_eligible
    ON nocmon.fault_impact_links (sla_credit_eligible);

-- =====================================================================
-- nocmon.topology_snapshots — cached topology blobs
-- =====================================================================
--
-- TC-NTV-005: query p95 < 3s for 50k nodes. We pre-materialize the
-- topology graph as a jsonb blob keyed by (scope, scope_id) so the
-- read path is a single-row SELECT. The TopologyBuilder port (wired
-- in cmd/nocmon-svc/main.go) is the only writer; node_count +
-- edge_count are denormalized for cheap dashboard headers.
CREATE TABLE nocmon.topology_snapshots (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    scope           TEXT NOT NULL
        CHECK (scope IN ('regional','branch','sub_area','olt')),
    scope_id        UUID,
    snapshot_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    payload         JSONB NOT NULL,
    node_count      INTEGER,
    edge_count      INTEGER,
    generated_by    TEXT NOT NULL DEFAULT 'system'
);

CREATE INDEX idx_nocmon_topo_scope_time
    ON nocmon.topology_snapshots (scope, scope_id, snapshot_at DESC);

-- =====================================================================
-- Permission catalog — 7 permissions under module 'nocmon'.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('nocmon', 'probe.read',         'View NOC service probes + samples'),
    ('nocmon', 'probe.write',        'Create / deactivate probes; record manual samples'),
    ('nocmon', 'fiber.read',         'View fiber link signal + attenuation history'),
    ('nocmon', 'fault.read',         'View NOC fault events + impact lists'),
    ('nocmon', 'fault.write',        'Open / link impact on fault events'),
    ('nocmon', 'fault.acknowledge',  'Acknowledge a NOC fault'),
    ('nocmon', 'fault.resolve',      'Mitigate / resolve a NOC fault'),
    ('nocmon', 'topology.read',      'Read cached topology snapshots'),
    ('nocmon', 'alert.wo.create',    'Convert a fault into a maintenance work order')
ON CONFLICT (module, action) DO NOTHING;

-- =====================================================================
-- Role catalog — three NOC roles.
-- =====================================================================
INSERT INTO identity.roles (name, description) VALUES
    ('noc_admin',    'NOC team admin — full probe + fault + topology authority'),
    ('noc_engineer', 'NOC engineer — operate probes + faults + create alert WO'),
    ('noc_viewer',   'NOC read-only — dashboard + reports only')
ON CONFLICT (name) DO NOTHING;

-- =====================================================================
-- Role → permission grants.
-- =====================================================================

-- super_admin gets every nocmon permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin' AND p.module = 'nocmon'
ON CONFLICT DO NOTHING;

-- noc_admin gets every nocmon permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc_admin' AND p.module = 'nocmon'
ON CONFLICT DO NOTHING;

-- noc_engineer gets probe.* + fiber.read + fault.* + topology.read + alert.wo.create.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc_engineer'
  AND p.module = 'nocmon'
  AND p.action IN (
      'probe.read','probe.write',
      'fiber.read',
      'fault.read','fault.write','fault.acknowledge','fault.resolve',
      'topology.read',
      'alert.wo.create'
  )
ON CONFLICT DO NOTHING;

-- noc_viewer gets read-only.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'noc_viewer'
  AND p.module = 'nocmon'
  AND p.action IN ('probe.read','fiber.read','fault.read','topology.read')
ON CONFLICT DO NOTHING;

-- =====================================================================
-- Demo seed — 2 probes + 1 fiber link bound to a demo customer.
-- =====================================================================
--
-- Fixed UUIDs so the seed is idempotent. The demo customer id is the
-- same one used by the broadband demo elsewhere
-- (00000000-0000-0000-0000-000000011201). If the broadband seed
-- hasn't loaded yet, the rows still insert — we don't FK to
-- crm.customers from this bounded context (decoupling).
INSERT INTO nocmon.service_probes
    (id, customer_id, probe_kind, probe_target, interval_seconds,
     threshold_warn, threshold_critical, is_active, last_status)
VALUES
    ('00000000-0000-0000-0000-000000011201',
     '00000000-0000-0000-0000-000000011201',
     'rtt', '8.8.8.8', 60,
     50.000, 100.000, TRUE, 'unknown'),
    ('00000000-0000-0000-0000-000000011202',
     '00000000-0000-0000-0000-000000011201',
     'packet_loss', '8.8.8.8', 60,
     1.000, 5.000, TRUE, 'unknown')
ON CONFLICT (id) DO NOTHING;

INSERT INTO nocmon.fiber_links
    (id, olt_port_id, onu_serial, expected_loss_db,
     warn_threshold_db, critical_threshold_db, status, customer_id)
VALUES
    ('00000000-0000-0000-0000-000000011203',
     '00000000-0000-0000-0000-000000011299',
     'DEMOONU0001', 22.50, 25.00, 28.00, 'unknown',
     '00000000-0000-0000-0000-000000011201')
ON CONFLICT (id) DO NOTHING;

COMMIT;
