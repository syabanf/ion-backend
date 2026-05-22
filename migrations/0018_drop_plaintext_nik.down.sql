-- Restoring the plaintext column on the way back is safe but the data
-- doesn't come back without a re-decrypt step. Operators going back to
-- 0017 should run a custom decrypt script if they need plaintext;
-- this migration only restores the schema shape.

BEGIN;

ALTER TABLE crm.leads     ADD COLUMN IF NOT EXISTS nik TEXT;
ALTER TABLE crm.customers ADD COLUMN IF NOT EXISTS nik TEXT;

COMMIT;
