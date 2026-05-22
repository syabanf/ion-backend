-- 0028_enterprise_negotiation.down.sql
BEGIN;

DROP TABLE IF EXISTS enterprise.negotiation_round_approvals;
DROP TABLE IF EXISTS enterprise.negotiation_rounds;
DROP TABLE IF EXISTS enterprise.negotiations;
DROP TABLE IF EXISTS enterprise.negotiation_participants;

ALTER TABLE enterprise.boq_versions
    DROP COLUMN IF EXISTS negotiation_config_locked_at,
    DROP COLUMN IF EXISTS negotiation_discount_ceiling,
    DROP COLUMN IF EXISTS negotiation_margin_floor,
    DROP COLUMN IF EXISTS pricing_adjustment_allowed,
    DROP COLUMN IF EXISTS negotiation_mode,
    DROP COLUMN IF EXISTS negotiation_type,
    DROP COLUMN IF EXISTS negotiation_enabled;

COMMIT;
