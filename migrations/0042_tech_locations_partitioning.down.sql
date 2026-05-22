-- 0042_tech_locations_partitioning.down.sql
--
-- Reverts 0042_tech_locations_partitioning.up.sql. Drops the BRIN
-- index, the retention helper function, and clears the table comment.
-- The base field.tech_locations table itself is untouched — that's
-- owned by migration 0040.

BEGIN;

-- Restore the original table comment (which was empty before 0042).
COMMENT ON TABLE field.tech_locations IS NULL;

-- Drop the retention helper. The function comment drops with the
-- function.
DROP FUNCTION IF EXISTS field.purge_tech_locations(INT);

-- Drop the BRIN index. The btree (user_id, captured_at DESC) from
-- 0040 stays in place.
DROP INDEX IF EXISTS field.idx_tech_loc_captured_brin;

COMMIT;
