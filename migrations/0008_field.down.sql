DELETE FROM identity.role_permissions
WHERE permission_id IN (SELECT id FROM identity.permissions WHERE module = 'field');
DELETE FROM identity.permissions WHERE module = 'field';

DROP TABLE IF EXISTS field.bast_records;
DROP TABLE IF EXISTS field.wo_resolution_items;
DROP TABLE IF EXISTS field.wo_checklist_responses;
DROP TABLE IF EXISTS field.wo_checklist_template_items;
DROP TABLE IF EXISTS field.wo_checklist_templates;
DROP TABLE IF EXISTS field.wo_reschedules;
DROP TABLE IF EXISTS field.wo_assignments;
DROP TABLE IF EXISTS field.work_orders;
DROP TABLE IF EXISTS field.team_members;
DROP TABLE IF EXISTS field.teams;

DROP SCHEMA IF EXISTS field CASCADE;
