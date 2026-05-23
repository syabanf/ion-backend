-- Wave 103 — rollback.
--
-- Drop the two new tables + the new permissions. We intentionally LEAVE
-- the `technician` role itself in place because other modules (broadband
-- field, etc.) may have granted it permissions outside this wave. If a
-- clean rollback is needed, drop it manually after running this down.

BEGIN;

-- Permission grants
DELETE FROM identity.role_permissions rp
USING identity.permissions p
WHERE rp.permission_id = p.id
  AND p.module = 'enterprise'
  AND p.action IN ('ewo.mobile.read', 'ewo.mobile.complete');

DELETE FROM identity.permissions
WHERE module = 'enterprise'
  AND action IN ('ewo.mobile.read', 'ewo.mobile.complete');

-- Tables (and their indexes drop automatically with the tables)
DROP TABLE IF EXISTS enterprise.ewo_push_log;
DROP TABLE IF EXISTS enterprise.ewo_checklist_progress;

COMMIT;
