-- Wave 113 down — drop the netdev schema. CASCADE so the seven tables +
-- indexes go with it. Safe because no other schema references netdev.*
-- (cross-context UUIDs are plain, not FKs). The permission rows in
-- identity.permissions stay — removing them would orphan any role_grants
-- a downstream wave might add; the netdev module disappears from the
-- catalog by being unselected when the schema is gone.
DROP SCHEMA IF EXISTS netdev CASCADE;
