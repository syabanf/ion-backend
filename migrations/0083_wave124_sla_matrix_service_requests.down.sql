-- Wave 124 — SLA Matrix + Service Requests rollback.

-- Drop the new permissions (and the role_permissions cascade with them).
DELETE FROM identity.role_permissions
 WHERE permission_id IN (
    SELECT id FROM identity.permissions
     WHERE module = 'cs'
       AND action IN (
        'sla.read','sla.manage',
        'service_request.read','service_request.submit',
        'service_request.approve','service_request.reject','service_request.fulfill',
        'team.read','team.manage','team.assign',
        'csat.read','csat.submit',
        'communication.read','communication.send',
        'wo.create_from_ticket'
       )
 );

DELETE FROM identity.permissions
 WHERE module = 'cs'
   AND action IN (
    'sla.read','sla.manage',
    'service_request.read','service_request.submit',
    'service_request.approve','service_request.reject','service_request.fulfill',
    'team.read','team.manage','team.assign',
    'csat.read','csat.submit',
    'communication.read','communication.send',
    'wo.create_from_ticket'
   );

-- New tables.
DROP TABLE IF EXISTS cs.communications              CASCADE;
DROP TABLE IF EXISTS cs.csat_responses              CASCADE;
DROP TABLE IF EXISTS cs.ticket_assignments_history  CASCADE;
DROP TABLE IF EXISTS cs.team_members                CASCADE;
DROP TABLE IF EXISTS cs.teams                       CASCADE;
DROP TABLE IF EXISTS cs.service_requests            CASCADE;
DROP TABLE IF EXISTS cs.sla_matrix                  CASCADE;

-- Roll back the ALTER on cs.tickets.
ALTER TABLE cs.tickets
    DROP COLUMN IF EXISTS sla_matrix_id,
    DROP COLUMN IF EXISTS sla_first_response_due_at,
    DROP COLUMN IF EXISTS sla_resolve_due_at,
    DROP COLUMN IF EXISTS sla_breached_first_response,
    DROP COLUMN IF EXISTS sla_breached_resolve,
    DROP COLUMN IF EXISTS sla_warned_at;
