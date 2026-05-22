-- 0026_enterprise_phase3.down.sql
BEGIN;

DROP TABLE IF EXISTS enterprise.approval_instances;
DROP TABLE IF EXISTS enterprise.boq_lines;
DROP TABLE IF EXISTS enterprise.boq_versions;
DROP TABLE IF EXISTS enterprise.approval_template_members;
DROP TABLE IF EXISTS enterprise.approval_templates;
DROP TABLE IF EXISTS enterprise.sla_templates;

-- Supplier extension columns — drop in reverse order of add. Existing
-- rows lose the priority/holding metadata; that's the down-migration
-- contract.
ALTER TABLE warehouse.suppliers
    DROP COLUMN IF EXISTS holding_company_id,
    DROP COLUMN IF EXISTS priority_score,
    DROP COLUMN IF EXISTS is_internal_vendor;

-- Permission rows kept (same reasoning as 0024 — deployers usually
-- want them to survive a re-up).

COMMIT;
