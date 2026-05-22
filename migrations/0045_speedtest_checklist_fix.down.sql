-- 0045_speedtest_checklist_fix.down.sql
--
-- Restores the pre-0045 CHECK on field.wo_checklist_template_items
-- (without 'speedtest'). NOTE: this will fail if any rows reference
-- item_type='speedtest' — clean those up before running.

BEGIN;

ALTER TABLE field.wo_checklist_template_items
    DROP CONSTRAINT IF EXISTS wo_checklist_template_items_item_type_check;
ALTER TABLE field.wo_checklist_template_items
    ADD CONSTRAINT wo_checklist_template_items_item_type_check
    CHECK (item_type IN (
        'photo','text','number','checkbox','qr_scan',
        'signature','gps_location','optical_power'
    ));

COMMIT;
