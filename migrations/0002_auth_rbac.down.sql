-- 0002 — DOWN: drop refresh tokens, clear permission grants and catalog.
BEGIN;

DROP TABLE IF EXISTS identity.refresh_tokens;

DELETE FROM identity.role_permissions;
DELETE FROM identity.permissions;

COMMIT;
