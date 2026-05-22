-- 0039 — sales quotas + project milestones (S-Curve) + small follow-ups.
--
-- Adds:
--   crm.sales_quotas              — per-rep monthly target (count + revenue)
--   enterprise.project_milestones — planned + actual progress for S-Curve calc
--   field.work_orders.wo_assignment_id passthrough hint (no new column)
--
-- Plus seed: a couple of demo quotas for sales@ion.local / sales-mgr@ion.local
-- so the leaderboard widget renders something on first open.

BEGIN;

-- =====================================================================
-- 1. Sales quotas (per-rep monthly targets)
-- =====================================================================
CREATE TABLE IF NOT EXISTS crm.sales_quotas (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL,                -- identity.users (soft FK)
    period_month    DATE NOT NULL,                -- first day of the quota month
    target_orders   INT NOT NULL DEFAULT 0,
    target_revenue  NUMERIC(14,2) NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, period_month)
);
CREATE INDEX IF NOT EXISTS idx_sales_quotas_user
    ON crm.sales_quotas (user_id, period_month DESC);

-- =====================================================================
-- 2. Project milestones — planned vs actual for S-Curve calc
-- =====================================================================
CREATE TABLE IF NOT EXISTS enterprise.project_milestones (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL,                -- enterprise.projects (soft FK)
    seq_no          INT NOT NULL,
    title           TEXT NOT NULL,
    description     TEXT,
    -- Planning curve
    planned_start   DATE NOT NULL,
    planned_end     DATE NOT NULL,
    planned_weight  NUMERIC(5,2) NOT NULL DEFAULT 0,    -- % of project total
    -- Actuals
    actual_start    DATE,
    actual_end      DATE,
    progress_pct    NUMERIC(5,2) NOT NULL DEFAULT 0,    -- 0..100 reported by tech / sales
    -- Audit
    updated_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, seq_no)
);
CREATE INDEX IF NOT EXISTS idx_project_milestones_project
    ON enterprise.project_milestones (project_id, seq_no);

-- =====================================================================
-- 3. Seed two demo quotas so the leaderboard renders content
-- =====================================================================
INSERT INTO crm.sales_quotas (user_id, period_month, target_orders, target_revenue)
SELECT u.id, date_trunc('month', NOW())::date, 8, 12000000
FROM identity.users u
WHERE u.email IN ('sales@ion.local', 'sales-mgr@ion.local')
ON CONFLICT (user_id, period_month) DO NOTHING;

-- =====================================================================
-- 4. Seed a small project + milestones for S-Curve demo
-- =====================================================================
DO $$
DECLARE
    proj_id UUID;
BEGIN
    -- Attach milestones to whichever project already exists. If the
    -- table is empty, we silently skip — the seed is best-effort demo
    -- data, not a hard fixture.
    SELECT id INTO proj_id FROM enterprise.projects ORDER BY created_at DESC LIMIT 1;
    IF proj_id IS NULL THEN
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM enterprise.project_milestones WHERE project_id = proj_id) THEN
        INSERT INTO enterprise.project_milestones (project_id, seq_no, title, planned_start, planned_end, planned_weight, progress_pct, actual_start) VALUES
            (proj_id, 1, 'Site survey + design',         NOW()::date - INTERVAL '20 days', NOW()::date - INTERVAL '15 days', 10, 100, NOW()::date - INTERVAL '20 days'),
            (proj_id, 2, 'Procurement + vendor PO',      NOW()::date - INTERVAL '15 days', NOW()::date -  INTERVAL '7 days', 20, 100, NOW()::date - INTERVAL '15 days'),
            (proj_id, 3, 'Cable & fiber pull',           NOW()::date -  INTERVAL '8 days', NOW()::date +  INTERVAL '0 days', 25,  80, NOW()::date -  INTERVAL '8 days'),
            (proj_id, 4, 'Equipment install + config',   NOW()::date +  INTERVAL '0 days', NOW()::date +  INTERVAL '7 days', 25,  20, NOW()::date),
            (proj_id, 5, 'Activation + customer signoff',NOW()::date +  INTERVAL '7 days', NOW()::date + INTERVAL '14 days', 20,   0, NULL);
    END IF;
END $$;

COMMIT;
