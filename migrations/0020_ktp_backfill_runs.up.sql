-- M7 hardening — track provenance of `cmd/ktp-backfill` runs.
--
-- Round-3 ran the backfill once per environment with no audit trail.
-- For production rollout the operator needs to be able to answer
-- "when did we last backfill, by whom, how many rows, did it error?"
-- without scraping logs.

BEGIN;

CREATE TABLE IF NOT EXISTS crm.ktp_backfill_runs (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at      TIMESTAMPTZ,
    leads_migrated    INTEGER NOT NULL DEFAULT 0,
    customers_migrated INTEGER NOT NULL DEFAULT 0,
    error_message     TEXT,
    -- triggered_by is free-form: an env-derived hostname, the operator's
    -- handle (KTP_BACKFILL_OPERATOR), or "automation" for CI runs.
    triggered_by      TEXT NOT NULL DEFAULT 'unknown'
);

CREATE INDEX IF NOT EXISTS idx_ktp_backfill_started
    ON crm.ktp_backfill_runs (started_at DESC);

COMMIT;
