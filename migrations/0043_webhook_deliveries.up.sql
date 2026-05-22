-- 0043 — webhook delivery idempotency store.
--
-- Used by pkg/webhookx to record every inbound webhook by its
-- provider-supplied event id. Re-deliveries collapse on the unique
-- (provider, event_id) index; the Middleware returns HTTP 200 with
-- a "duplicate":true response so the provider's retry loop stops.
--
-- The body is preserved for forensics — if a downstream handler
-- crashed mid-processing we can replay manually.

BEGIN;

CREATE TABLE IF NOT EXISTS platform.webhook_deliveries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider    TEXT NOT NULL,          -- 'xendit' | 'meta' | 'mekari' | …
    event_id    TEXT NOT NULL,          -- provider-supplied delivery id
    body        BYTEA,                  -- raw POST body (capped at MaxBodyBytes upstream)
    remote_ip   TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,           -- set by the handler on success
    process_error TEXT,                 -- set if the handler errored
    UNIQUE (provider, event_id)
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_recv
    ON platform.webhook_deliveries (received_at DESC);

COMMENT ON TABLE platform.webhook_deliveries IS
    'Idempotency + audit store for every inbound webhook. '
    'See pkg/webhookx — the Verifier middleware inserts here on '
    'first sight; duplicate (provider, event_id) writes collapse on '
    'the unique index and the response becomes 200 with duplicate=true.';

COMMIT;
