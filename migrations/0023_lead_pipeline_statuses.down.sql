-- 0023_lead_pipeline_statuses.down.sql
--
-- Restore the original constraint. Any rows written with the new
-- statuses (active/warm/hot) must be remapped first or the rollback
-- will fail at constraint-validate time.
BEGIN;

ALTER TABLE crm.leads
    DROP CONSTRAINT IF EXISTS leads_status_check;

ALTER TABLE crm.leads
    ADD CONSTRAINT leads_status_check
    CHECK (status IN ('new','qualified','potential','rejected','converted','lost'));

COMMIT;
