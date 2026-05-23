-- Wave 92 — drop the multi-company holding scaffolding.

BEGIN;

-- Permissions / grants — remove the grants first, then the permission row.
DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'enterprise' AND action = 'holding_company.read'
);

DELETE FROM identity.permissions
WHERE module = 'enterprise' AND action = 'holding_company.read';

-- Triggers (dropped implicitly with the tables, but explicit is safer
-- when a follow-up migration only drops one of them).
DROP TRIGGER IF EXISTS trg_subsidiaries_touch ON enterprise.subsidiaries;
DROP TRIGGER IF EXISTS trg_holding_companies_touch ON enterprise.holding_companies;

DROP TABLE IF EXISTS enterprise.subsidiaries;
DROP TABLE IF EXISTS enterprise.holding_companies;

COMMIT;
