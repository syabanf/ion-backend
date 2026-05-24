-- Wave 128 down — drop the v2 DEFAULT billing schema and re-publish v1.
-- Idempotent: if v2 was never applied, the UPDATEs no-op cleanly.

BEGIN;

-- 1. Remove v2.
DELETE FROM platform.schema_definitions
 WHERE kind       = 'billing'
   AND code       = 'DEFAULT'
   AND version_no = 2;

-- 2. Restore v1 to published if it was superseded by the up migration.
UPDATE platform.schema_definitions
   SET status        = 'published',
       superseded_at = NULL
 WHERE kind          = 'billing'
   AND code          = 'DEFAULT'
   AND version_no    = 1
   AND status        = 'superseded';

COMMIT;
