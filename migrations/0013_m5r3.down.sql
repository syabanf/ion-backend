DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE (module = 'identity' AND action IN ('availability.read','availability.manage'))
       OR (module = 'shared'   AND action = 'upload.write')
);
DELETE FROM identity.permissions
WHERE (module = 'identity' AND action IN ('availability.read','availability.manage'))
   OR (module = 'shared'   AND action = 'upload.write');

DROP TABLE IF EXISTS shared.photo_uploads;
DROP SCHEMA IF EXISTS shared CASCADE;
DROP TABLE IF EXISTS identity.user_availability;
