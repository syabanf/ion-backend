-- 0042 — field.tech_locations growth strategy.
--
-- At steady state (400 active techs × 20s ping → 20 pings/sec ≈ 52M
-- rows/month) the original single-table layout would force VACUUM
-- pain + index bloat after 90 days. This migration adds:
--
--   1. BRIN index on captured_at (small, ideal for append-only)
--   2. A retention helper function for ops to call from cron
--   3. Comment block explaining when to flip to LIST partitioning
--      (left as a follow-up — partitioning needs a maintenance
--       window because we re-create as a partitioned table).
--
-- Why not partition right now: at 0–20M rows the BRIN index is
-- already enough; a partition switch is a 30-min lock that we don't
-- want to take without a real performance signal. The function below
-- gives us the surgical purge tool we'd reach for first.
--
-- The new gps_streaming_test.go in backend/test/perf monitors this
-- table's behaviour under load; ops should run it monthly.

BEGIN;

-- ============================================================
-- 1. BRIN on captured_at — the natural sort dimension
-- ============================================================
-- BRIN is ~1000x smaller than B-tree on append-only timestamps.
-- The existing btree on (user_id, captured_at DESC) stays — it's
-- needed for "latest ping per tech" lookups. The BRIN helps when
-- ops needs a range scan ("everything in the last 6 hours").

CREATE INDEX IF NOT EXISTS idx_tech_loc_captured_brin
    ON field.tech_locations USING BRIN (captured_at)
    WITH (pages_per_range = 32);

-- ============================================================
-- 2. Retention helper — purge anything older than N days
--
-- Usage from cron (run weekly):
--
--   SELECT field.purge_tech_locations(90);
--
-- Returns the number of rows deleted so the caller can log it.
-- ============================================================

CREATE OR REPLACE FUNCTION field.purge_tech_locations(retention_days INT)
RETURNS INT
LANGUAGE plpgsql
AS $$
DECLARE
    purged INT;
BEGIN
    IF retention_days < 7 THEN
        RAISE EXCEPTION 'retention_days must be >= 7 (got %)', retention_days;
    END IF;
    DELETE FROM field.tech_locations
    WHERE captured_at < NOW() - (retention_days || ' days')::interval;
    GET DIAGNOSTICS purged = ROW_COUNT;
    RETURN purged;
END $$;

COMMENT ON FUNCTION field.purge_tech_locations(INT) IS
    'Purge tech_locations rows older than the given number of days. '
    'Used by ops cron; run weekly. The function is intentionally a '
    'separate code path from any application-level write — partition '
    'pruning can replace it later without touching app code.';

-- ============================================================
-- 3. Future partitioning plan — embedded in DB comments so the
--    runbook lives next to the schema.
-- ============================================================

COMMENT ON TABLE field.tech_locations IS
$$Live GPS pings from technicians during active WOs.

Growth math: ~20 pings/sec sustained × 86400 s = 1.7M rows/day,
~52M rows/month at full national fleet (8 branches × 50 techs).

Current strategy (this migration): single table + BRIN on
captured_at + scheduled purge via field.purge_tech_locations().

When to flip to LIST partitioning (deferred until we see signal):
- Table size > 50 GB, OR
- Vacuum freeze causing user-visible latency, OR
- Need to drop a month's worth of pings as a single DDL.

Flip script (run in a maintenance window):
  ALTER TABLE field.tech_locations RENAME TO tech_locations_legacy;
  CREATE TABLE field.tech_locations (...same schema...)
      PARTITION BY RANGE (captured_at);
  CREATE TABLE field.tech_locations_y2026m05
      PARTITION OF field.tech_locations
      FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
  -- + one partition per month going forward
  INSERT INTO field.tech_locations SELECT * FROM tech_locations_legacy;
  DROP TABLE tech_locations_legacy;
$$;

COMMIT;
