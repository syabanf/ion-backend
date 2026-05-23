-- Wave 93 — Tax compliance context teardown.
--
-- The schema is owned exclusively by this wave (no cross-context FKs
-- in / out), so a CASCADE drop is safe and removes both tables in one
-- shot.
DROP SCHEMA IF EXISTS tax CASCADE;
