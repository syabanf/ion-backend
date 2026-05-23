-- Wave 88 (Tier 3) — Stock alert escalation state.
--
-- The existing AlertRepository.ListBelowThreshold computes the branch
-- escalation path (Sub Area → Area → Regional) but doesn't track HOW
-- LONG a warehouse-item pair has been below threshold. PRD §10 wants
-- alerts to auto-escalate up the branch chain on a time budget:
--
--   sub_area  → after 24h still below → bump to area
--   area      → after 24h still at area level → bump to regional
--
-- We can't infer "how long below" from stock_levels.updated_at because
-- that column is bumped on every movement, even ones that keep us
-- below. So we persist explicit state per (warehouse_id, stock_item_id):
--   open_since        — when this pair first crossed below threshold
--   current_level     — sub_area | area | regional
--   last_escalated_at — when current_level last changed
--
-- The cron worker (Wave 88 follow-up; this migration just lands the
-- table) walks open rows and bumps current_level when the time budget
-- expires.

CREATE TABLE warehouse.stock_alert_states (
    warehouse_id        UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE CASCADE,
    stock_item_id       UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE CASCADE,
    open_since          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    current_level       TEXT NOT NULL DEFAULT 'sub_area'
        CHECK (current_level IN ('sub_area','area','regional')),
    last_escalated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Tracks the audit message the cron last attached to a notify.
    -- Helpful for the dashboard to render "last notified: 2h ago".
    last_notified_at    TIMESTAMPTZ,
    closed_at           TIMESTAMPTZ,
    PRIMARY KEY (warehouse_id, stock_item_id)
);

CREATE INDEX idx_alert_state_open
    ON warehouse.stock_alert_states (open_since)
    WHERE closed_at IS NULL;

-- The cron's hot path scans by current_level + last_escalated_at to
-- find rows past the time budget. Partial index keeps it cheap.
CREATE INDEX idx_alert_state_cascade
    ON warehouse.stock_alert_states (current_level, last_escalated_at)
    WHERE closed_at IS NULL;
