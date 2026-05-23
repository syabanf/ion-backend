-- Wave 124 — SLA Matrix + Service Requests + Team Assignment + WO-from-Ticket
--                + CSAT + Communications (Phase 1C Broadband).
--
-- Builds on Wave 123's cs.tickets foundation. Adds:
--   • cs.sla_matrix         — per (customer_type × ticket_type × priority)
--                              first-response + resolve minutes lookup,
--                              effective-dated.
--   • cs.tickets SLA cols   — snapshot of matrix lookup at ticket open;
--                              breach flags + warned_at for cron loop.
--   • cs.service_requests   — first-class service-request entity linked
--                              to a ticket; submitted/approved/in_progress/
--                              fulfilled/cancelled state machine.
--   • cs.teams + cs.team_members — agent grouping for round-robin /
--                              team-level ticket assignment.
--   • cs.ticket_assignments_history — append-only audit row per assign.
--   • cs.csat_responses     — 1-to-1 to ticket; rating 1-5 + channel.
--   • cs.communications     — every inbound/outbound message logged
--                              (email/whatsapp/sms/call/portal).
--
-- TC families targeted (~33):
--   • TC-PSL-* / TC-SLA-*  — Priority & SLA (10)
--   • TC-SR-*              — Service Requests (12)
--   • TC-TA-*              — Team Assignment (8)
--   • TC-WFT-*             — WO from Ticket (5)
--   • TC-CSAT-*            — CSAT (4)
--   • TC-COM-* (cs comm)   — Customer Communications (6)
--
-- Coordination:
--   • Wave 125 (Bulk Ops in internal/operations/) is disjoint.

-- =====================================================================
-- 1. cs.sla_matrix — per (customer_type × ticket_type × priority) SLA
-- =====================================================================
CREATE TABLE cs.sla_matrix (
    id                        UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_type             TEXT NOT NULL
        CHECK (customer_type IN ('residential','business','enterprise','reseller','internal')),
    ticket_type               TEXT NOT NULL
        CHECK (ticket_type IN ('technical','billing','complaint','service_request','information')),
    priority                  TEXT NOT NULL
        CHECK (priority IN ('low','normal','high','urgent')),
    first_response_minutes    INT NOT NULL CHECK (first_response_minutes > 0),
    resolve_minutes           INT NOT NULL CHECK (resolve_minutes > 0),
    breach_warn_pct           NUMERIC(4,3) NOT NULL DEFAULT 0.80
        CHECK (breach_warn_pct > 0 AND breach_warn_pct < 1),
    escalation_levels         JSONB NOT NULL DEFAULT '[]'::jsonb,
    is_active                 BOOLEAN NOT NULL DEFAULT TRUE,
    effective_from            DATE NOT NULL,
    effective_to              DATE,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (customer_type, ticket_type, priority, effective_from)
);

CREATE INDEX idx_cs_sla_matrix_lookup
    ON cs.sla_matrix(customer_type, ticket_type, priority, is_active);

CREATE INDEX idx_cs_sla_matrix_effective
    ON cs.sla_matrix(customer_type, ticket_type, priority, effective_from DESC)
    WHERE is_active = TRUE;

-- =====================================================================
-- 2. cs.tickets — SLA columns (ALTER, not full rewrite)
--
-- sla_matrix_id is a soft FK (no REFERENCES) so we can drop matrix
-- rows without breaking historical tickets — the snapshot fields
-- (sla_first_response_due_at, sla_resolve_due_at) carry the authoritative
-- values from the moment the ticket was opened.
-- =====================================================================
ALTER TABLE cs.tickets
    ADD COLUMN sla_matrix_id                 UUID,
    ADD COLUMN sla_first_response_due_at     TIMESTAMPTZ,
    ADD COLUMN sla_resolve_due_at            TIMESTAMPTZ,
    ADD COLUMN sla_breached_first_response   BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN sla_breached_resolve          BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN sla_warned_at                 TIMESTAMPTZ;

CREATE INDEX idx_cs_tickets_sla_breach
    ON cs.tickets(sla_breached_resolve, sla_resolve_due_at)
    WHERE status NOT IN ('resolved','closed');

CREATE INDEX idx_cs_tickets_sla_first_response_due
    ON cs.tickets(sla_first_response_due_at)
    WHERE first_response_at IS NULL AND status NOT IN ('resolved','closed');

-- =====================================================================
-- 3. cs.service_requests — first-class service-request entity
-- =====================================================================
CREATE TABLE cs.service_requests (
    id                         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id                  UUID NOT NULL REFERENCES cs.tickets(id) ON DELETE RESTRICT,
    customer_id                UUID NOT NULL,
    request_type               TEXT NOT NULL
        CHECK (request_type IN ('plan_change','address_relocation','add_on',
                                'suspend_pause','speed_upgrade','speed_downgrade',
                                'equipment_swap','vacation_hold','data','other')),
    reference_id               UUID,
    status                     TEXT NOT NULL DEFAULT 'submitted'
        CHECK (status IN ('submitted','approved','rejected','in_progress',
                          'fulfilled','cancelled')),
    submitted_by               UUID,
    approved_by                UUID,
    approval_decision_at       TIMESTAMPTZ,
    rejection_reason           TEXT,
    fulfilled_at               TIMESTAMPTZ,
    cancelled_reason           TEXT,
    sla_due_at                 TIMESTAMPTZ,
    payload                    JSONB,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cs_service_requests_customer ON cs.service_requests(customer_id, status);
CREATE INDEX idx_cs_service_requests_pending  ON cs.service_requests(status, sla_due_at);
CREATE INDEX idx_cs_service_requests_ticket   ON cs.service_requests(ticket_id);

-- =====================================================================
-- 4. cs.teams + cs.team_members — agent grouping
-- =====================================================================
CREATE TABLE cs.teams (
    id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name                  TEXT NOT NULL UNIQUE,
    description           TEXT,
    manager_user_id       UUID,
    members_count         INT NOT NULL DEFAULT 0,
    focus_ticket_types    TEXT[] NOT NULL DEFAULT '{}',
    is_active             BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE cs.team_members (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    team_id         UUID NOT NULL REFERENCES cs.teams(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL,
    role_in_team    TEXT NOT NULL DEFAULT 'agent'
        CHECK (role_in_team IN ('agent','lead','manager','backup')),
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at         TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_cs_team_members_active_unique
    ON cs.team_members(team_id, user_id)
    WHERE left_at IS NULL;

CREATE INDEX idx_cs_team_members_user ON cs.team_members(user_id, team_id);
CREATE INDEX idx_cs_team_members_role ON cs.team_members(team_id, role_in_team)
    WHERE left_at IS NULL;

-- Seed 4 demo teams.
INSERT INTO cs.teams (name, description, focus_ticket_types) VALUES
    ('tech-support',     'L1/L2 technical incident response',         ARRAY['technical']),
    ('billing-support',  'Billing & invoice disputes',                 ARRAY['billing']),
    ('escalation',       'Supervisor-tier complaints + escalations',   ARRAY['complaint']),
    ('ops-l2',           'Operations L2 — service requests + info',    ARRAY['service_request','information'])
ON CONFLICT (name) DO NOTHING;

-- =====================================================================
-- 5. cs.ticket_assignments_history — append-only assign audit
-- =====================================================================
CREATE TABLE cs.ticket_assignments_history (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id         UUID NOT NULL REFERENCES cs.tickets(id) ON DELETE CASCADE,
    assignment_kind   TEXT NOT NULL
        CHECK (assignment_kind IN ('user','team','transfer','unassign')),
    from_user_id      UUID,
    to_user_id        UUID,
    from_team_id      UUID,
    to_team_id        UUID,
    reason            TEXT,
    assigned_by       UUID,
    assigned_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cs_assign_history_ticket   ON cs.ticket_assignments_history(ticket_id, assigned_at DESC);
CREATE INDEX idx_cs_assign_history_to_user  ON cs.ticket_assignments_history(to_user_id) WHERE to_user_id IS NOT NULL;
CREATE INDEX idx_cs_assign_history_to_team  ON cs.ticket_assignments_history(to_team_id) WHERE to_team_id IS NOT NULL;

-- =====================================================================
-- 6. cs.csat_responses — 1-to-1 ticket survey response
-- =====================================================================
CREATE TABLE cs.csat_responses (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id       UUID NOT NULL UNIQUE REFERENCES cs.tickets(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL,
    rating          INT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment         TEXT,
    channel         TEXT NOT NULL DEFAULT 'email'
        CHECK (channel IN ('email','whatsapp','sms','portal','inapp')),
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    responded_at    TIMESTAMPTZ
);

CREATE INDEX idx_cs_csat_customer  ON cs.csat_responses(customer_id, responded_at DESC);
CREATE INDEX idx_cs_csat_rating    ON cs.csat_responses(rating, responded_at DESC);

-- =====================================================================
-- 7. cs.communications — outbound + inbound message log per ticket
-- =====================================================================
CREATE TABLE cs.communications (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id               UUID REFERENCES cs.tickets(id) ON DELETE CASCADE,
    kind                    TEXT NOT NULL
        CHECK (kind IN ('email_in','email_out','whatsapp_in','whatsapp_out',
                        'sms_in','sms_out','call_log','portal_msg')),
    direction               TEXT NOT NULL
        CHECK (direction IN ('inbound','outbound')),
    counterparty_kind       TEXT NOT NULL DEFAULT 'customer'
        CHECK (counterparty_kind IN ('customer','agent','supervisor','external','system')),
    counterparty_id         UUID,
    counterparty_label      TEXT,
    subject                 TEXT,
    body                    TEXT,
    attachments             JSONB NOT NULL DEFAULT '[]'::jsonb,
    external_message_id     TEXT,
    sent_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at            TIMESTAMPTZ,
    read_at                 TIMESTAMPTZ,
    error_msg               TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cs_comm_ticket   ON cs.communications(ticket_id, sent_at DESC);
CREATE INDEX idx_cs_comm_external ON cs.communications(external_message_id)
    WHERE external_message_id IS NOT NULL;

-- =====================================================================
-- 8. Permissions
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('cs', 'sla.read',                  'View SLA matrix + per-ticket SLA status'),
    ('cs', 'sla.manage',                'CRUD SLA matrix rows'),
    ('cs', 'service_request.read',      'View service requests'),
    ('cs', 'service_request.submit',    'Submit a new service request'),
    ('cs', 'service_request.approve',   'Approve a pending service request'),
    ('cs', 'service_request.reject',    'Reject a pending service request'),
    ('cs', 'service_request.fulfill',   'Fulfill an in-progress service request'),
    ('cs', 'team.read',                 'View CS teams + members'),
    ('cs', 'team.manage',               'Create / edit CS teams + members'),
    ('cs', 'team.assign',               'Assign tickets to a team'),
    ('cs', 'csat.read',                 'View CSAT responses + aggregations'),
    ('cs', 'csat.submit',               'Record a CSAT response (typically customer)'),
    ('cs', 'communication.read',        'View ticket communication timeline'),
    ('cs', 'communication.send',        'Log outbound + receive inbound communications'),
    ('cs', 'wo.create_from_ticket',     'Create a WO from a CS ticket')
ON CONFLICT DO NOTHING;

-- super_admin → all new cs permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'cs'
ON CONFLICT DO NOTHING;

-- cs_supervisor — everything except sla.manage + team.manage (those are
-- operations_admin).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'cs_supervisor'
  AND p.module = 'cs'
  AND p.action IN (
    'sla.read', 'service_request.read', 'service_request.approve',
    'service_request.reject', 'service_request.fulfill',
    'team.read', 'team.assign',
    'csat.read',
    'communication.read', 'communication.send',
    'wo.create_from_ticket'
  )
ON CONFLICT DO NOTHING;

-- cs_agent — most reads + submit/send + create-wo
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'cs_agent'
  AND p.module = 'cs'
  AND p.action IN (
    'sla.read',
    'service_request.read', 'service_request.submit', 'service_request.fulfill',
    'team.read',
    'csat.read',
    'communication.read', 'communication.send',
    'wo.create_from_ticket'
  )
ON CONFLICT DO NOTHING;

-- operations_admin — SLA + team management
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND p.module = 'cs'
  AND p.action IN ('sla.read','sla.manage','team.read','team.manage','team.assign','csat.read')
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 9. Seed the SLA matrix — 5 customer_types × 5 ticket_types × 4 priorities
--                          = 100 rows. effective_from = current date so
--                          lookups by effective_from <= NOW() always hit.
--
-- Defaults by priority (in minutes):
--   urgent : first 15  / resolve 240    (4h)
--   high   : first 30  / resolve 480    (8h)
--   normal : first 120 / resolve 1440   (24h)
--   low    : first 240 / resolve 4320   (72h)
--
-- Ticket-type multipliers on resolve (first-response is priority-only):
--   technical       : 1.0
--   billing         : 1.5   (research-heavy)
--   complaint       : 2.0   (escalation-prone)
--   service_request : 1.5   (cross-team co-ordination)
--   information     : 0.5   (quick answer)
--
-- Customer-type multipliers (both first-response and resolve):
--   residential : 1.0
--   business    : 0.7  (tighter SLA)
--   enterprise  : 0.5
--   reseller    : 0.6
--   internal    : 1.2
--
-- The numbers below were computed once with all 3 multipliers applied
-- then rounded to integer minutes. They're sensible Phase-1 defaults
-- — admin can override per-row via the matrix CRUD route.
-- =====================================================================
DO $$
DECLARE
    today DATE := CURRENT_DATE;
BEGIN
    INSERT INTO cs.sla_matrix
        (customer_type, ticket_type, priority,
         first_response_minutes, resolve_minutes, effective_from)
    VALUES
        -- residential × all ticket types × 4 priorities
        ('residential','technical','urgent',15,240,today),
        ('residential','technical','high',30,480,today),
        ('residential','technical','normal',120,1440,today),
        ('residential','technical','low',240,4320,today),
        ('residential','billing','urgent',15,360,today),
        ('residential','billing','high',30,720,today),
        ('residential','billing','normal',120,2160,today),
        ('residential','billing','low',240,6480,today),
        ('residential','complaint','urgent',15,480,today),
        ('residential','complaint','high',30,960,today),
        ('residential','complaint','normal',120,2880,today),
        ('residential','complaint','low',240,8640,today),
        ('residential','service_request','urgent',15,360,today),
        ('residential','service_request','high',30,720,today),
        ('residential','service_request','normal',120,2160,today),
        ('residential','service_request','low',240,6480,today),
        ('residential','information','urgent',15,120,today),
        ('residential','information','high',30,240,today),
        ('residential','information','normal',120,720,today),
        ('residential','information','low',240,2160,today),

        -- business × all ticket types × 4 priorities (×0.7)
        ('business','technical','urgent',10,168,today),
        ('business','technical','high',21,336,today),
        ('business','technical','normal',84,1008,today),
        ('business','technical','low',168,3024,today),
        ('business','billing','urgent',10,252,today),
        ('business','billing','high',21,504,today),
        ('business','billing','normal',84,1512,today),
        ('business','billing','low',168,4536,today),
        ('business','complaint','urgent',10,336,today),
        ('business','complaint','high',21,672,today),
        ('business','complaint','normal',84,2016,today),
        ('business','complaint','low',168,6048,today),
        ('business','service_request','urgent',10,252,today),
        ('business','service_request','high',21,504,today),
        ('business','service_request','normal',84,1512,today),
        ('business','service_request','low',168,4536,today),
        ('business','information','urgent',10,84,today),
        ('business','information','high',21,168,today),
        ('business','information','normal',84,504,today),
        ('business','information','low',168,1512,today),

        -- enterprise × all ticket types × 4 priorities (×0.5)
        ('enterprise','technical','urgent',8,120,today),
        ('enterprise','technical','high',15,240,today),
        ('enterprise','technical','normal',60,720,today),
        ('enterprise','technical','low',120,2160,today),
        ('enterprise','billing','urgent',8,180,today),
        ('enterprise','billing','high',15,360,today),
        ('enterprise','billing','normal',60,1080,today),
        ('enterprise','billing','low',120,3240,today),
        ('enterprise','complaint','urgent',8,240,today),
        ('enterprise','complaint','high',15,480,today),
        ('enterprise','complaint','normal',60,1440,today),
        ('enterprise','complaint','low',120,4320,today),
        ('enterprise','service_request','urgent',8,180,today),
        ('enterprise','service_request','high',15,360,today),
        ('enterprise','service_request','normal',60,1080,today),
        ('enterprise','service_request','low',120,3240,today),
        ('enterprise','information','urgent',8,60,today),
        ('enterprise','information','high',15,120,today),
        ('enterprise','information','normal',60,360,today),
        ('enterprise','information','low',120,1080,today),

        -- reseller × all ticket types × 4 priorities (×0.6)
        ('reseller','technical','urgent',9,144,today),
        ('reseller','technical','high',18,288,today),
        ('reseller','technical','normal',72,864,today),
        ('reseller','technical','low',144,2592,today),
        ('reseller','billing','urgent',9,216,today),
        ('reseller','billing','high',18,432,today),
        ('reseller','billing','normal',72,1296,today),
        ('reseller','billing','low',144,3888,today),
        ('reseller','complaint','urgent',9,288,today),
        ('reseller','complaint','high',18,576,today),
        ('reseller','complaint','normal',72,1728,today),
        ('reseller','complaint','low',144,5184,today),
        ('reseller','service_request','urgent',9,216,today),
        ('reseller','service_request','high',18,432,today),
        ('reseller','service_request','normal',72,1296,today),
        ('reseller','service_request','low',144,3888,today),
        ('reseller','information','urgent',9,72,today),
        ('reseller','information','high',18,144,today),
        ('reseller','information','normal',72,432,today),
        ('reseller','information','low',144,1296,today),

        -- internal × all ticket types × 4 priorities (×1.2)
        ('internal','technical','urgent',18,288,today),
        ('internal','technical','high',36,576,today),
        ('internal','technical','normal',144,1728,today),
        ('internal','technical','low',288,5184,today),
        ('internal','billing','urgent',18,432,today),
        ('internal','billing','high',36,864,today),
        ('internal','billing','normal',144,2592,today),
        ('internal','billing','low',288,7776,today),
        ('internal','complaint','urgent',18,576,today),
        ('internal','complaint','high',36,1152,today),
        ('internal','complaint','normal',144,3456,today),
        ('internal','complaint','low',288,10368,today),
        ('internal','service_request','urgent',18,432,today),
        ('internal','service_request','high',36,864,today),
        ('internal','service_request','normal',144,2592,today),
        ('internal','service_request','low',288,7776,today),
        ('internal','information','urgent',18,144,today),
        ('internal','information','high',36,288,today),
        ('internal','information','normal',144,864,today),
        ('internal','information','low',288,2592,today)
    ON CONFLICT (customer_type, ticket_type, priority, effective_from) DO NOTHING;
END$$;
