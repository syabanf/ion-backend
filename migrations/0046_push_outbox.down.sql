-- 0046_push_outbox.down.sql
--
-- Drops the push notification audit/outbox table. Losing this is
-- forensic-only (no live state depends on it). Re-applying 0046.up
-- recreates the table; existing pushes can be re-derived from
-- application logs but not from the schema.

BEGIN;

DROP TABLE IF EXISTS platform.push_outbox;

COMMIT;
