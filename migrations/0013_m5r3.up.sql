-- M5 Round 3 — HRIS availability stub, photo uploads, GPS check.
--
-- Round-3 scope:
--   * identity.user_availability — per-user, per-day status stub. Real
--     HRIS integration is round-4; for now we store rows directly so
--     the Team Leader roster view has something to render.
--   * shared.photo_uploads — metadata for files uploaded via the new
--     /uploads endpoint. The actual file bytes live on local disk in
--     round 3 (path stored as object_url); round-4 swaps to MinIO/S3.
--   * Auto-pair-on-SLA-breach watcher: no schema change — it scans
--     existing tables.
--
-- Deferred to round-4:
--   * HRIS sync job (real external API)
--   * MinIO/S3 storage
--   * Photo signed-URL flow

-- =====================================================================
-- 1. identity.user_availability
--
-- One row per (user_id, date). We allow exactly one status per day so
-- the roster view is unambiguous. Notes can hold a leave reason etc.
-- =====================================================================
CREATE TABLE identity.user_availability (
    user_id   UUID NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    date      DATE NOT NULL,
    status    TEXT NOT NULL CHECK (status IN ('available', 'leave', 'sick', 'training', 'off')),
    notes     TEXT,
    updated_by UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, date)
);
CREATE INDEX idx_user_availability_date
    ON identity.user_availability (date)
    WHERE status <> 'available';

COMMENT ON TABLE identity.user_availability IS
    'Daily availability status per user. Stub for HRIS integration: '
    'rows are set manually via the Team Leader / Operations admin UI '
    'until round-4 wires the external HRIS sync.';

-- =====================================================================
-- 2. shared.photo_uploads
--
-- Lives in a separate `shared` schema so any context (field BAST,
-- warehouse intake later, etc.) can record uploads in one place.
-- We persist enough metadata (GPS, EXIF tags, uploader, content type)
-- to surface verification info without re-parsing the file later.
-- =====================================================================
CREATE SCHEMA IF NOT EXISTS shared;

CREATE TABLE shared.photo_uploads (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    object_url      TEXT NOT NULL,        -- file:///… or s3://… in round-4
    content_type    TEXT NOT NULL,
    bytes           BIGINT NOT NULL CHECK (bytes >= 0),
    -- EXIF / metadata captured at upload time so the consumer can
    -- verify gps_required without re-reading the file.
    gps_lat         DOUBLE PRECISION,
    gps_lng         DOUBLE PRECISION,
    gps_accuracy_m  DOUBLE PRECISION,
    taken_at        TIMESTAMPTZ,
    sha256          TEXT,                 -- content hash for dedupe
    uploaded_by     UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_photo_uploads_by  ON shared.photo_uploads (uploaded_by, uploaded_at DESC);
CREATE INDEX idx_photo_uploads_sha ON shared.photo_uploads (sha256) WHERE sha256 IS NOT NULL;

-- =====================================================================
-- 3. Permission seeds
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('identity', 'availability.read',  'View user daily availability'),
    ('identity', 'availability.manage','Set user daily availability'),
    ('shared',   'upload.write',       'Upload files to the shared object store')
ON CONFLICT (module, action) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND ((p.module = 'identity' AND p.action IN ('availability.read','availability.manage'))
       OR (p.module = 'shared'   AND p.action = 'upload.write'))
ON CONFLICT DO NOTHING;

-- Team Leader: full availability surface + upload (for completing BASTs).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'team_leader'
  AND ((p.module = 'identity' AND p.action IN ('availability.read','availability.manage'))
       OR (p.module = 'shared'   AND p.action = 'upload.write'))
ON CONFLICT DO NOTHING;

-- Operations admin: read availability + upload (for ad-hoc fixes).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'operations_admin'
  AND ((p.module = 'identity' AND p.action IN ('availability.read','availability.manage'))
       OR (p.module = 'shared'   AND p.action = 'upload.write'))
ON CONFLICT DO NOTHING;

-- Technicians: upload (photos for checklist responses).
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'technician'
  AND p.module = 'shared' AND p.action = 'upload.write'
ON CONFLICT DO NOTHING;

-- HR-style roles (none yet in P1; placeholder for round-4 HR module).
-- finance_* / cs_* / noc_* don't need this surface.
