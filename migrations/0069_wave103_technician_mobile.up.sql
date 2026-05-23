-- Wave 103 — Technician mobile API for EWO assignment.
--
-- Surface:
--   - Mobile routes under /api/mobile/ewos/* scoped to the authenticated
--     technician (claims.UserID). EWO-Y rows only — the executing side is
--     the only side a technician ever sees; the EWO-X commercial side
--     stays inside the head office.
--   - Per-EWO checklist progress, separate from the static checklist
--     template (`enterprise.ewo_checklist_items`). Progress rows carry an
--     idempotency_key so the mobile app's offline-queue replay is safe.
--   - Push log persistence so the cron dispatcher can answer "did we
--     already notify this user for this EWO?" without duplicating the
--     payload.
--
-- Tables created:
--   - enterprise.ewo_checklist_progress
--   - enterprise.ewo_push_log
--
-- Permissions added:
--   - enterprise.ewo.mobile.read
--   - enterprise.ewo.mobile.complete
--
-- Role grants: `technician` role gets both perms. The role was already
-- created by an earlier seed if present; we INSERT … ON CONFLICT DO
-- NOTHING so this migration is idempotent against either start state.

BEGIN;

-- =====================================================================
-- enterprise.ewo_checklist_progress — per-EWO instance state for the
-- technician mobile app. Distinct from `enterprise.ewo_checklist_items`
-- (the static template) because:
--   - The template predates the dual-EWO + mobile workflow; we don't want
--     to mutate its schema or grant the mobile permission against it.
--   - Mobile clients need idempotency_key for offline-queue replay; the
--     legacy checklist items table has no such column.
--   - Progress rows carry a photo URL + photo hash (proof-of-work) that
--     the legacy table doesn't model.
-- =====================================================================

CREATE TABLE enterprise.ewo_checklist_progress (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ewo_id              UUID NOT NULL
        REFERENCES enterprise.ewos(id) ON DELETE CASCADE,
    checklist_item_id   UUID,
    item_label          TEXT,
    status              TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'done', 'skipped', 'blocked')),
    completed_by        UUID,
    completed_at        TIMESTAMPTZ,
    photo_url           TEXT,
    photo_hash          TEXT,
    notes               TEXT,
    -- Idempotency key — when non-null, the (ewo_id, idempotency_key)
    -- unique constraint stops a replayed offline write from inserting
    -- a duplicate row. Null is allowed (legacy / non-mobile writes).
    idempotency_key     TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Unique key — replay-safety. Postgres allows multiple NULL values in
-- a UNIQUE constraint, so non-mobile writes with NULL idempotency_key
-- still work fine.
CREATE UNIQUE INDEX idx_ewo_checklist_progress_idempotency
    ON enterprise.ewo_checklist_progress (ewo_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX idx_ewo_checklist_progress_ewo_status
    ON enterprise.ewo_checklist_progress (ewo_id, status);

CREATE INDEX idx_ewo_checklist_progress_completed_by
    ON enterprise.ewo_checklist_progress (completed_by, completed_at DESC);

-- =====================================================================
-- enterprise.ewo_push_log — durable record of mobile push notifications
-- fired by the technician cron dispatcher. Two uses:
--   1. Idempotency — the cron checks for a pre-existing row before
--      firing a duplicate push (assignment notification fires once,
--      reschedule fires once per change).
--   2. Audit / UI — the mobile app's "what did I get pushed?" surface
--      reads this table for the current user's recent pushes.
-- Append-only — there is no update path.
-- =====================================================================

CREATE TABLE enterprise.ewo_push_log (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    ewo_id             UUID NOT NULL,
    subject            TEXT NOT NULL
        CHECK (subject IN ('assigned', 'reassigned', 'reschedule', 'reminder', 'cancelled')),
    target_user_id     UUID NOT NULL,
    payload            JSONB,
    sent_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatch_status    TEXT NOT NULL DEFAULT 'sent',
    error_msg          TEXT
);

CREATE INDEX idx_ewo_push_log_user_sent
    ON enterprise.ewo_push_log (target_user_id, sent_at DESC);

CREATE INDEX idx_ewo_push_log_ewo_sent
    ON enterprise.ewo_push_log (ewo_id, sent_at DESC);

-- Used by the cron to ask "have I already sent an `assigned` push for
-- (ewo_id, target_user_id)?". Partial index keeps it small even after
-- the log fills up — only the one-shot subjects are deduplicated here;
-- `reminder` is allowed to repeat.
CREATE INDEX idx_ewo_push_log_dedup
    ON enterprise.ewo_push_log (ewo_id, target_user_id, subject)
    WHERE subject IN ('assigned', 'reassigned', 'reschedule', 'cancelled');

-- =====================================================================
-- RBAC — new permissions + technician role grant.
--
-- A `technician` role may already exist (broadband side seeded one in an
-- earlier migration); we don't create it here because the broadband
-- definition is the canonical one. We DO grant the two new mobile
-- permissions to it if it exists. The DELETE-on-rollback path keeps the
-- role itself in place — it's owned upstream.
-- =====================================================================

INSERT INTO identity.permissions (module, action, description) VALUES
    ('enterprise', 'ewo.mobile.read',     'Mobile: read EWOs assigned to me + my checklist progress'),
    ('enterprise', 'ewo.mobile.complete', 'Mobile: mark my checklist items done / skipped / blocked')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin gets every new permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'enterprise'
  AND p.action IN ('ewo.mobile.read', 'ewo.mobile.complete')
ON CONFLICT DO NOTHING;

-- Create the `technician` role if it doesn't exist — Phase 1 enterprise
-- introduces it as a distinct role from the broadband `field_tech` /
-- `installer` flavor. Idempotent.
INSERT INTO identity.roles (name, description) VALUES
    ('technician', 'Enterprise field technician — receives EWO-Y assignments and reports back from the mobile app')
ON CONFLICT (name) DO NOTHING;

-- technician gets both mobile perms.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'enterprise'
  AND p.action IN ('ewo.mobile.read', 'ewo.mobile.complete')
ON CONFLICT DO NOTHING;

-- team_lead also gets read on the mobile surface so a TL can preview a
-- technician's checklist from the same routes.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_lead'
  AND p.module = 'enterprise'
  AND p.action = 'ewo.mobile.read'
ON CONFLICT DO NOTHING;

COMMIT;
