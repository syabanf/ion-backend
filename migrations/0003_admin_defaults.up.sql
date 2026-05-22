-- 0003 — Seed platform_config defaults referenced across modules.
--
-- These keys are read at runtime by various services. Every key in PRD
-- Administration §9 that has a default lives here. Setting them up via
-- migration ensures a fresh DB is immediately operable without manual
-- bootstrap steps.
--
-- Format: config_value is stored as text. Callers parse to the expected
-- type. JSON values are stored as their compact JSON string.

BEGIN;

INSERT INTO identity.platform_config (config_key, config_value) VALUES
    -- Inventory (Warehouse §4 + Admin §9.9)
    ('inventory_valuation_method', 'fifo'),

    -- Map provider (Admin §9.4)
    ('map_provider',          'google_maps'),

    -- QR label format (Admin §9.1)
    ('qr_code_format',        '{type}/{branch_code}/{serial}'),

    -- Network (Admin §9.7)
    ('fiber_warning_dbm',     '-25'),
    ('fiber_critical_dbm',    '-28'),
    ('port_reservation_timeout_hours', '48'),

    -- Tax (Admin §9.10)
    ('ppn_rate',              '11'),
    ('faktur_pajak_series',   ''),
    ('company_npwp',          ''),
    ('company_name',          'PT ION Network Indonesia'),

    -- Cable distance defaults (per-branch overrides live on branches.cable_distance)
    ('cable_max_run_meters',  '210'),
    ('cable_route_factor',    '1.3'),
    ('cable_excess_price_per_meter', '0'),

    -- WO assignment SLA defaults (per-branch overrides on branches.wo_auto_assign)
    ('wo_sla_high_minutes',   '30'),
    ('wo_sla_medium_minutes', '120'),
    ('wo_sla_low_minutes',    '240'),

    -- ION Radius integration (Admin §9.6)
    ('ion_radius_sync_interval_seconds', '60'),

    -- HRIS integration (Admin §9.6)
    ('hris_sync_interval_minutes', '15')
ON CONFLICT (config_key) DO NOTHING;

-- ----------------------------------------------------------------------
-- Add a human-readable description on audit_logs.
--
-- QA fed back that the prior build's audit viewer was "kurang bisa dibaca
-- manusia" — too much raw before/after JSON. We'll let writers attach a
-- short description ("Created user jane@ion.co.id", "Deactivated Sub Area
-- BDG-UTR-01") so the UI has something to render alongside the diff.
-- ----------------------------------------------------------------------
ALTER TABLE identity.audit_logs
    ADD COLUMN IF NOT EXISTS description TEXT,
    ADD COLUMN IF NOT EXISTS metadata    JSONB;

COMMIT;
