-- 0023_lead_pipeline_statuses.up.sql
--
-- PRD §6.3 line 1427 specifies the lead status set as:
--   New / Active / Warm / Hot / Converted / Lost / Potential
--
-- The original 0007_crm migration shipped with a CHECK constraint
-- allowing only `new / qualified / potential / rejected / converted /
-- lost` — derived from a coverage-driven flow rather than the
-- sales-pipeline flow the PRD describes. QA flagged this against
-- TC-CRM-001 and TC-CRM-013: "saat pertama create langsung jadi
-- potential" — the auto-promotion in ApplyCoverage stamps `potential`
-- on creation when excess is accepted, denying the operator the
-- New → Active → Warm → Hot funnel progression.
--
-- This migration:
--   1. Adds `active`, `warm`, `hot` to the allowed set so the domain
--      enum can stop using `qualified` as a stand-in for "Active".
--   2. Keeps `qualified` and `rejected` in the allowed set so legacy
--      rows aren't invalidated. They become deprecated synonyms — the
--      code stops *writing* them, but the DB still accepts reads.
--
-- The auto-promotion behavior itself is fixed in the domain code, not
-- here — this migration just lifts the constraint that would have
-- rejected the new enum values.

BEGIN;

ALTER TABLE crm.leads
    DROP CONSTRAINT IF EXISTS leads_status_check;

ALTER TABLE crm.leads
    ADD CONSTRAINT leads_status_check
    CHECK (status IN (
        'new',
        'active',
        'warm',
        'hot',
        -- legacy / coverage-driven states kept for back-compat with
        -- rows written before 0023. New writes shouldn't use these.
        'qualified',
        'rejected',
        -- potential is in the PRD set (excess-distance customers who
        -- agreed to pay for extra cable) — not deprecated.
        'potential',
        'converted',
        'lost'
    ));

COMMIT;
