-- 0039_quota_scurve.down.sql
--
-- Reverts 0039_quota_scurve.up.sql. Drops the two new tables
-- (sales_quotas + project_milestones). Seed data goes with the
-- tables — both seeds are demo-only fixtures, not production state.
--
-- WARNING: dropping enterprise.project_milestones loses ALL S-Curve
-- planning data. Production rollback should export first.
--
-- The migration 0044 cron added enterprise.project_milestones
-- .invoice_triggered_at lazily at runtime (not in a migration file).
-- If 0044 has been applied and the column was created, the DROP TABLE
-- here removes it too — no special handling needed.

BEGIN;

-- 1. Drop project_milestones (indexes + the lazy invoice_triggered_at
-- column drop with the parent).
DROP TABLE IF EXISTS enterprise.project_milestones;

-- 2. Drop sales_quotas (index drops with parent).
DROP TABLE IF EXISTS crm.sales_quotas;

COMMIT;
