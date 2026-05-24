-- Wave 128D — CS ticket importer foundation.
--
-- Closes the divergence between field.tickets (legacy 5-state SM from
-- Wave 67/D7) and cs.tickets (canonical 7-state SM from Wave 123). The
-- Wave 123 MIGRATION.md note said "future importer wave will backfill
-- cs.tickets from field.tickets"; the Wave 127 closeout report deferred
-- it to "earliest Wave 130". Wave 128D is that wave — ships now.
--
-- Schema change is minimal: cs.tickets gets a nullable legacy_id column
-- with a partial unique index so the importer's ON CONFLICT
-- (legacy_id) DO NOTHING upsert is idempotent on re-run. NULL is
-- allowed (and excluded from the unique index) so existing cs-native
-- tickets keep working without backfill.
--
-- The importer logic itself lives in internal/cs/usecase/importer.go;
-- the cron in internal/cs/cron/cron.go schedules a daily tick; an
-- admin-triggered HTTP route lives at POST /api/cs/importer/run.
--
-- TC family covered (~6 TCs):
--   • TC-IMP-001..006 — Ticket SM importer (status mapping, category
--     → ticket_type mapping, legacy_id idempotence, re-run safety).

BEGIN;

-- =====================================================================
-- 1. cs.tickets.legacy_id — soft FK back to field.tickets.id
--
-- Nullable so existing cs-native rows (created via Wave 123 portal +
-- agent flows) don't need a backfill. The partial unique index
-- enforces idempotence: each legacy ticket can be imported at most
-- once, but rows with legacy_id IS NULL (cs-native) coexist freely.
-- =====================================================================
ALTER TABLE cs.tickets
    ADD COLUMN IF NOT EXISTS legacy_id UUID;

CREATE UNIQUE INDEX IF NOT EXISTS uniq_cs_tickets_legacy_id
    ON cs.tickets (legacy_id)
    WHERE legacy_id IS NOT NULL;

-- =====================================================================
-- 2. Permissions — admin-triggered importer run
--
-- POST /api/cs/importer/run is gated by cs.importer.run. Granted to
-- super_admin (the catch-all) + operations_admin (the role that already
-- runs batch ops). cs_supervisor is intentionally NOT granted — the
-- importer touches cross-schema legacy data and should be admin-only.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('cs.importer', 'run', 'Trigger the field.tickets → cs.tickets importer')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
  FROM identity.roles r
  CROSS JOIN identity.permissions p
 WHERE r.name IN ('super_admin','operations_admin')
   AND p.module = 'cs.importer'
   AND p.action = 'run'
ON CONFLICT (role_id, permission_id) DO NOTHING;

COMMIT;
