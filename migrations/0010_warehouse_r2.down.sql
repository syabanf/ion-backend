DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'warehouse' AND action IN ('threshold.manage','alerts.read')
);
DELETE FROM identity.permissions
WHERE module = 'warehouse' AND action IN ('threshold.manage','alerts.read');

DROP TABLE IF EXISTS warehouse.opname_counts;
DROP TABLE IF EXISTS warehouse.opname_sessions;
