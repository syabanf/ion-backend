-- =====================================================================
-- Down migration for 0071_wave106_cpq_polish.up.sql
--
-- Drops in reverse:
--   3. boq_versions.commercial_owner_subsidiary_id + its index
--   2. pre_boq_required_fields table (drops seed rows with it)
--   1. pricebook_lines.priority_score + its index
-- =====================================================================

-- 3. BOQ column.
DROP INDEX IF EXISTS enterprise.idx_boq_versions_commercial_owner;
ALTER TABLE enterprise.boq_versions
    DROP COLUMN IF EXISTS commercial_owner_subsidiary_id;

-- 2. Pre-BOQ required fields config.
DROP INDEX IF EXISTS enterprise.idx_pre_boq_required_fields_position;
DROP TABLE IF EXISTS enterprise.pre_boq_required_fields;

-- 1. Pricebook line priority.
DROP INDEX IF EXISTS enterprise.idx_pricebook_lines_priority;
ALTER TABLE enterprise.pricebook_lines
    DROP COLUMN IF EXISTS priority_score;
