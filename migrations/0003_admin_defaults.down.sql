-- 0003 — DOWN: revert audit_logs schema changes, remove platform_config seeds.
BEGIN;

ALTER TABLE identity.audit_logs
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS metadata;

DELETE FROM identity.platform_config WHERE config_key IN (
    'inventory_valuation_method',
    'map_provider',
    'qr_code_format',
    'fiber_warning_dbm',
    'fiber_critical_dbm',
    'port_reservation_timeout_hours',
    'ppn_rate',
    'faktur_pajak_series',
    'company_npwp',
    'company_name',
    'cable_max_run_meters',
    'cable_route_factor',
    'cable_excess_price_per_meter',
    'wo_sla_high_minutes',
    'wo_sla_medium_minutes',
    'wo_sla_low_minutes',
    'ion_radius_sync_interval_seconds',
    'hris_sync_interval_minutes'
);

COMMIT;
