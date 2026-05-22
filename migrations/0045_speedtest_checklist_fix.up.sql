-- 0045 — fix the speedtest checklist item_type constraint.
--
-- Migration 0041 attempted to extend the item_type CHECK on
-- field.wo_checklist_template_items to include 'speedtest', but the
-- DO block referenced the wrong constraint name
-- ('checklist_template_items_item_type_check' instead of
-- 'wo_checklist_template_items_item_type_check'), so the EXISTS guard
-- never matched and the ALTER never ran.
--
-- This migration re-applies it with the correct name. The speedtest
-- parser endpoint (GET /work-orders/{id}/speedtest) and the tech app's
-- speedtest checklist item depend on the constraint allowing the new
-- item_type, otherwise inserts would fail with a check_violation.

BEGIN;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.check_constraints
    WHERE constraint_name = 'wo_checklist_template_items_item_type_check'
  ) THEN
    ALTER TABLE field.wo_checklist_template_items
        DROP CONSTRAINT wo_checklist_template_items_item_type_check;
  END IF;

  ALTER TABLE field.wo_checklist_template_items
      ADD CONSTRAINT wo_checklist_template_items_item_type_check
      CHECK (item_type IN (
          'photo','text','number','checkbox','qr_scan',
          'signature','gps_location','optical_power',
          'speedtest'
      ));
END$$;

COMMIT;
