-- Wave 123 — Customer Service bounded-context foundation (Phase 1C).
--
-- Lays the canonical home for the CS module that until now lived
-- scattered across internal/field/adapter/http/phase2.go (agent
-- ticket CRUD) and internal/crm/adapter/http/portal_auth.go
-- (customer-portal CS submit). The legacy paths stay live for
-- backwards compatibility; a future wave will write a one-shot
-- importer to backfill cs.tickets from field.tickets.
--
-- TC families covered (~26 TCs):
--   • TC-TT-* / TC-TKT-* — Ticket Types (8)
--   • TC-TL-*            — Ticket Lifecycle (10) — 7-state SM
--   • TC-TCH-* / TC-CHN-* — Ticket Channels (6)
--   • TC-MEN-*           — @Mentions foundation (3)
--
-- Coordination:
--   • Wave 124 (SLA Matrix) will ALTER cs.tickets to add sla_due_at,
--     sla_template_id, sla_breached. Index/column layout below leaves
--     room for those additions.
--   • Wave 125 (Bulk Ops executors) is disjoint — touches
--     internal/operations/ only, no cs.* dependency.

-- =====================================================================
-- 1. cs schema
-- =====================================================================
CREATE SCHEMA IF NOT EXISTS cs;

-- =====================================================================
-- 2. cs.tickets — canonical CS ticket aggregate
--
-- 7-state lifecycle:
--   open → assigned → in_progress → pending_customer | pending_internal
--     → resolved → closed (terminal; reopen from resolved/closed back
--     to in_progress increments escalation_level/pause counters in app
--     code).
--
-- ticket_type is the workflow taxonomy (technical / billing / complaint
-- / service_request / information) — distinct from field.tickets.category
-- which is symptom-oriented. The two stores remain divergent; the
-- importer in a later wave will best-effort map.
--
-- pause_seconds + paused_since power the SLA-on-pause calc (Wave 124
-- will read pause_seconds via the EffectiveAge helper).
-- =====================================================================
CREATE TABLE cs.tickets (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_no           TEXT NOT NULL UNIQUE,
    customer_id         UUID NOT NULL,
    opened_by           UUID NOT NULL,
    opened_via          TEXT NOT NULL
        CHECK (opened_via IN ('portal','whatsapp','phone','email','walkin',
                              'agent_internal','api','tech_app')),
    ticket_type         TEXT NOT NULL
        CHECK (ticket_type IN ('technical','billing','complaint',
                               'service_request','information')),
    title               TEXT NOT NULL,
    description         TEXT,
    status              TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','assigned','in_progress','pending_customer',
                          'pending_internal','resolved','closed')),
    priority            TEXT NOT NULL DEFAULT 'normal'
        CHECK (priority IN ('low','normal','high','urgent')),
    assigned_user_id    UUID,
    assigned_team_id    UUID,
    first_response_at   TIMESTAMPTZ,
    resolved_at         TIMESTAMPTZ,
    closed_at           TIMESTAMPTZ,
    escalated_at        TIMESTAMPTZ,
    escalation_level    INT NOT NULL DEFAULT 0,
    related_wo_id       UUID,
    related_invoice_id  UUID,
    pause_seconds       BIGINT NOT NULL DEFAULT 0,
    paused_since        TIMESTAMPTZ,
    source_metadata     JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cs_tickets_customer_status   ON cs.tickets(customer_id, status);
CREATE INDEX idx_cs_tickets_assignee_status   ON cs.tickets(assigned_user_id, status);
CREATE INDEX idx_cs_tickets_team_status       ON cs.tickets(assigned_team_id, status);
CREATE INDEX idx_cs_tickets_queue             ON cs.tickets(status, priority, created_at DESC);
CREATE INDEX idx_cs_tickets_ticket_no         ON cs.tickets(ticket_no);

-- =====================================================================
-- 3. cs.ticket_events — append-only timeline / audit per ticket
-- =====================================================================
CREATE TABLE cs.ticket_events (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id   UUID NOT NULL REFERENCES cs.tickets(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL
        CHECK (kind IN ('status_change','priority_change','assignment',
                        'reassignment','comment','attachment','mention',
                        'close','reopen','escalation','sla_breach',
                        'channel_change')),
    payload     JSONB,
    actor_id    UUID,
    actor_role  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cs_ticket_events_ticket ON cs.ticket_events(ticket_id, created_at DESC);
CREATE INDEX idx_cs_ticket_events_kind   ON cs.ticket_events(kind, created_at DESC);

-- =====================================================================
-- 4. cs.ticket_comments — agent + internal note thread
-- =====================================================================
CREATE TABLE cs.ticket_comments (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id    UUID NOT NULL REFERENCES cs.tickets(id) ON DELETE CASCADE,
    author_id    UUID NOT NULL,
    author_role  TEXT,
    body         TEXT NOT NULL,
    is_internal  BOOLEAN NOT NULL DEFAULT FALSE,
    attachments  JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at    TIMESTAMPTZ,
    deleted_at   TIMESTAMPTZ
);

CREATE INDEX idx_cs_ticket_comments_ticket ON cs.ticket_comments(ticket_id, created_at DESC);

-- =====================================================================
-- 5. cs.ticket_mentions — @username pings against identity.users
--
-- One row per (comment, mentioned user). UNIQUE on (comment_id,
-- mentioned_user_id) so re-parsing the same body is idempotent.
-- =====================================================================
CREATE TABLE cs.ticket_mentions (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id               UUID NOT NULL REFERENCES cs.tickets(id) ON DELETE CASCADE,
    comment_id              UUID REFERENCES cs.ticket_comments(id) ON DELETE CASCADE,
    mentioned_user_id       UUID NOT NULL,
    mentioned_by_user_id    UUID NOT NULL,
    read_at                 TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (comment_id, mentioned_user_id)
);

CREATE INDEX idx_cs_ticket_mentions_user
    ON cs.ticket_mentions(mentioned_user_id, read_at, created_at DESC);

-- =====================================================================
-- 6. cs.ticket_attachments — file uploads attached to ticket or comment
-- =====================================================================
CREATE TABLE cs.ticket_attachments (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ticket_id          UUID NOT NULL REFERENCES cs.tickets(id) ON DELETE CASCADE,
    comment_id         UUID REFERENCES cs.ticket_comments(id) ON DELETE CASCADE,
    file_url           TEXT,
    file_name          TEXT,
    file_size_bytes    BIGINT,
    file_hash          TEXT,
    uploaded_by        UUID,
    uploaded_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cs_ticket_attachments_ticket
    ON cs.ticket_attachments(ticket_id, uploaded_at DESC);

-- =====================================================================
-- 7. cs.ticket_channels — channel taxonomy
--
-- The opened_via enum lives on cs.tickets directly, but the channel
-- registry below holds the per-channel display name + activation flag
-- + per-channel config (e.g. WhatsApp template id). Admin can disable
-- a channel without touching the enum.
-- =====================================================================
CREATE TABLE cs.ticket_channels (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL
        CHECK (kind IN ('inbound','outbound','both')),
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    config_payload  JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO cs.ticket_channels (code, name, kind, is_active) VALUES
    ('portal',         'Customer Portal',  'inbound',  TRUE),
    ('whatsapp',       'WhatsApp',         'both',     TRUE),
    ('phone',          'Phone Call',       'inbound',  TRUE),
    ('email',          'Email',            'both',     TRUE),
    ('walkin',         'Walk-In',          'inbound',  TRUE),
    ('agent_internal', 'Agent Internal',   'outbound', TRUE),
    ('api',            'API',              'both',     TRUE),
    ('tech_app',       'Technician App',   'inbound',  TRUE)
ON CONFLICT (code) DO NOTHING;

-- =====================================================================
-- 8. Permissions — additive grants for the CS surface
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('cs', 'ticket.read',     'View CS tickets'),
    ('cs', 'ticket.write',    'Create / edit CS tickets'),
    ('cs', 'ticket.assign',   'Assign / reassign CS tickets'),
    ('cs', 'ticket.close',    'Close resolved CS tickets'),
    ('cs', 'ticket.reopen',   'Reopen resolved or closed CS tickets'),
    ('cs', 'ticket.escalate', 'Escalate CS tickets'),
    ('cs', 'comment.read',    'View CS ticket comments'),
    ('cs', 'comment.write',   'Post CS ticket comments'),
    ('cs', 'comment.edit',    'Edit own CS ticket comments'),
    ('cs', 'comment.delete',  'Delete own CS ticket comments'),
    ('cs', 'mention.read',    'View @mentions addressed to self'),
    ('cs', 'channel.read',    'View CS ticket channels'),
    ('cs', 'channel.manage',  'Manage CS ticket channels')
ON CONFLICT DO NOTHING;

-- super_admin → all cs permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'cs'
ON CONFLICT DO NOTHING;

-- cs_supervisor — all cs permissions except channel.manage
INSERT INTO identity.roles (name, description) VALUES
    ('cs_supervisor', 'CS supervisor — manages CS agent queue and ticket escalations')
ON CONFLICT (name) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'cs_supervisor'
  AND p.module = 'cs'
  AND p.action <> 'channel.manage'
ON CONFLICT DO NOTHING;

-- cs_agent — ticket read/write + comment read/write + mention.read
INSERT INTO identity.roles (name, description) VALUES
    ('cs_agent', 'CS agent — handles customer tickets')
ON CONFLICT (name) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'cs_agent'
  AND p.module = 'cs'
  AND p.action IN ('ticket.read','ticket.write','comment.read','comment.write',
                   'comment.edit','mention.read')
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 9. Seed — 3 demo tickets (one per priority tier) for the demo customer
--
-- Picks the first crm.customers row. If the customers table has no rows
-- (fresh CI run), the seed silently no-ops.
-- =====================================================================
DO $$
DECLARE
    demo_customer UUID;
    demo_user     UUID;
    t1_id         UUID := uuid_generate_v4();
    t2_id         UUID := uuid_generate_v4();
    t3_id         UUID := uuid_generate_v4();
BEGIN
    SELECT id INTO demo_customer FROM crm.customers ORDER BY created_at ASC LIMIT 1;
    SELECT id INTO demo_user FROM identity.users ORDER BY created_at ASC LIMIT 1;

    IF demo_customer IS NULL OR demo_user IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO cs.tickets
        (id, ticket_no, customer_id, opened_by, opened_via, ticket_type,
         title, description, status, priority)
    VALUES
        (t1_id, 'TKT-2026-00000001', demo_customer, demo_user, 'portal',     'technical',       'No internet connection',     'Customer reports total outage since 8am',       'open',        'urgent'),
        (t2_id, 'TKT-2026-00000002', demo_customer, demo_user, 'whatsapp',   'billing',         'Invoice mismatch',           'Charged twice for May invoice',                  'assigned',    'high'),
        (t3_id, 'TKT-2026-00000003', demo_customer, demo_user, 'phone',      'service_request', 'Plan upgrade enquiry',       'Asks about upgrading to Fiber 50',               'in_progress', 'normal')
    ON CONFLICT (ticket_no) DO NOTHING;

    INSERT INTO cs.ticket_events (ticket_id, kind, payload, actor_id, actor_role)
    VALUES
        (t1_id, 'status_change', jsonb_build_object('to','open'),       demo_user, 'system'),
        (t2_id, 'status_change', jsonb_build_object('to','open'),       demo_user, 'system'),
        (t2_id, 'assignment',    jsonb_build_object('assignee_user_id', demo_user::text), demo_user, 'cs_supervisor'),
        (t2_id, 'status_change', jsonb_build_object('to','assigned'),   demo_user, 'cs_supervisor'),
        (t3_id, 'status_change', jsonb_build_object('to','open'),       demo_user, 'system'),
        (t3_id, 'status_change', jsonb_build_object('to','in_progress'),demo_user, 'cs_agent')
    ON CONFLICT DO NOTHING;
END$$;
