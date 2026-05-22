-- 0046 — push notification audit log (outbox pattern).
--
-- Every notifyx.Dispatcher.Send call drops a row here. With the stub
-- provider in place this gives ops a queryable trail of "what would
-- have been pushed", and when the real FCM/APNS adapter lands the
-- same writes happen — the only difference is `delivered_at` flips
-- from NULL to NOW() on a successful send (vs. the stub which just
-- logs and exits).
--
-- The table is intentionally append-only with a retention purge in
-- the platform-janitor cron (see enterprise/cron/cron.go). Sized
-- generously: ~50k rows/month would be a busy deployment.

BEGIN;

CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.push_outbox (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- One of: 'user' (single recipient via user_id),
    -- 'customer' (single recipient via customer_id),
    -- 'users' (bulk fan-out — recipient_ids holds the set).
    target_kind   TEXT NOT NULL CHECK (target_kind IN ('user','customer','users')),
    user_id       UUID,                       -- when target_kind='user'
    customer_id   UUID,                       -- when target_kind='customer'
    recipient_ids UUID[],                     -- when target_kind='users'

    -- The message envelope, exactly as the dispatcher saw it. We
    -- store the payload as columns (vs. one jsonb blob) so ops can
    -- filter without parsing.
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    deep_link     TEXT,
    topic         TEXT,
    data          JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Dispatcher metadata.
    provider      TEXT NOT NULL DEFAULT 'stub',  -- 'stub' | 'fcm' | 'apns' | …
    queued_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at  TIMESTAMPTZ,                   -- nil until provider confirms
    delivery_error TEXT                          -- set if provider returned err
);

-- Recent-first scans (admin UI, debugging).
CREATE INDEX IF NOT EXISTS idx_push_outbox_queued
    ON platform.push_outbox (queued_at DESC);

-- "Show me all pushes to this user". Single-recipient case only;
-- bulk fan-outs are still findable via recipient_ids = ANY(...).
CREATE INDEX IF NOT EXISTS idx_push_outbox_user
    ON platform.push_outbox (user_id, queued_at DESC)
    WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_push_outbox_customer
    ON platform.push_outbox (customer_id, queued_at DESC)
    WHERE customer_id IS NOT NULL;

-- Un-delivered pushes (when a real provider is wired). Cheap partial
-- index that the dispatcher can poll if we ever add a redrive worker.
CREATE INDEX IF NOT EXISTS idx_push_outbox_pending
    ON platform.push_outbox (queued_at)
    WHERE delivered_at IS NULL;

COMMENT ON TABLE platform.push_outbox IS
    'Audit + outbox of every push dispatched via pkg/notifyx. '
    'Stub provider writes a row + sets delivered_at on success; FCM '
    'adapter does the same but with a real provider call between. '
    'Retention: 30 days, purged by the platform-janitor cron.';

COMMIT;
