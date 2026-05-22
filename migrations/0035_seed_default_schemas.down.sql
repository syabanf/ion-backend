-- Roll back the DEFAULT-code published schemas seeded in 0035.up.sql.
-- We narrowly target the seeded rows so any operator-created drafts
-- with code='DEFAULT' (unlikely but possible) survive the roll-back.

DELETE FROM platform.schema_definitions
WHERE code = 'DEFAULT'
  AND version_no = 1
  AND status = 'published'
  AND kind IN ('billing', 'commission', 'suspension', 'service');
