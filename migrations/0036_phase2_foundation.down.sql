-- Reverse of 0036_phase2_foundation.up.sql. Drops in FK-safe order.

BEGIN;

-- Reverse the checklist CHECK constraint back to its 7-value form.
ALTER TABLE field.wo_checklist_template_items
    DROP CONSTRAINT IF EXISTS wo_checklist_template_items_item_type_check;
ALTER TABLE field.wo_checklist_template_items
    ADD CONSTRAINT wo_checklist_template_items_item_type_check
    CHECK (item_type IN (
        'photo','text','number','checkbox','qr_scan','signature','gps_location'
    ));

-- Drop the WO link columns.
ALTER TABLE field.work_orders
    DROP COLUMN IF EXISTS ticket_id,
    DROP COLUMN IF EXISTS maintenance_event_id;

-- Permissions cleanup. role_permissions cascades when permissions drop.
DELETE FROM identity.permissions WHERE module || '.' || action IN (
    'crm.addon.read','crm.addon.manage','crm.addon.sell',
    'crm.plan_change.create','crm.plan_change.decide',
    'crm.relocation.create','crm.relocation.decide',
    'field.ticket.read','field.ticket.create',
    'field.ticket.assign','field.ticket.resolve',
    'field.maintenance.read','field.maintenance.create','field.maintenance.dispatch'
);

DROP TABLE IF EXISTS field.maintenance_event_nodes;
DROP TABLE IF EXISTS field.maintenance_events;
DROP TABLE IF EXISTS field.tickets;
DROP TABLE IF EXISTS crm.customer_relocations;
DROP TABLE IF EXISTS crm.plan_change_requests;
DROP TABLE IF EXISTS crm.customer_addons;
DROP TABLE IF EXISTS crm.product_addons;

COMMIT;
