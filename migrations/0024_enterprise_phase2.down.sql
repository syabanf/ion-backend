-- 0024_enterprise_phase2.down.sql
BEGIN;

DROP TABLE IF EXISTS enterprise.opportunities;
DROP TABLE IF EXISTS enterprise.pricebook_lines;
DROP TABLE IF EXISTS enterprise.pricebooks;
DROP FUNCTION IF EXISTS enterprise.touch_updated_at();
DROP SCHEMA IF EXISTS enterprise;

-- Note: we deliberately do NOT clean up the permission rows here.
-- If a deployer rolls this back, they likely want to keep the
-- perms for a re-up. They can DELETE manually if needed.

COMMIT;
