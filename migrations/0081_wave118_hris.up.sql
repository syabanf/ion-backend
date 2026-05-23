-- Wave 118 — HRIS Integration (Phase 2B operationalization).
--
-- Closes the 12 HRIS TCs from the Wave 110 audit:
--   • hris.employees (canonical FK target — replaces the unstructured
--     identity.users.employee_id label)
--   • hris.employee_events (ingest queue: hired / transferred / promoted /
--     resigned / suspended / reinstated / role_changed / salary_changed)
--   • identity.users.hris_employee_no FK to hris.employees(employee_no)
--   • Permissions + role grants (super_admin / hr_admin / finance_admin)
--
-- Bounded-context rule: hris.* is its own schema. Cross-context references
-- (branch_id, commissions) are plain UUIDs — Wave 114 orchestration consults
-- HRIS via the HRISResignedReader port. No FK across schemas.

-- =====================================================================
-- 1. hris schema
-- =====================================================================
CREATE SCHEMA IF NOT EXISTS hris;

-- =====================================================================
-- 2. hris.employees — canonical employee record
-- =====================================================================
CREATE TABLE hris.employees (
    id                          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    employee_no                 TEXT NOT NULL UNIQUE,
    full_name                   TEXT NOT NULL,
    email                       TEXT,
    phone                       TEXT,
    department                  TEXT,
    position                    TEXT,
    manager_employee_no         TEXT,
    hire_date                   DATE,
    resign_date                 DATE,
    status                      TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','resigned','suspended','probation')),
    kyc_completed               BOOLEAN NOT NULL DEFAULT FALSE,
    npwp                        TEXT,
    bank_account_no             TEXT,
    branch_id                   UUID,
    role_recommendations        JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_hris_employees_status         ON hris.employees(status);
CREATE INDEX idx_hris_employees_branch_status  ON hris.employees(branch_id, status);
CREATE INDEX idx_hris_employees_manager        ON hris.employees(manager_employee_no);

-- =====================================================================
-- 3. hris.employee_events — ingest queue
-- =====================================================================
CREATE TABLE hris.employee_events (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    employee_no         TEXT NOT NULL,
    event_kind          TEXT NOT NULL
        CHECK (event_kind IN ('hired','transferred','promoted','resigned',
                              'suspended','reinstated','role_changed','salary_changed')),
    event_payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at         TIMESTAMPTZ NOT NULL,
    ingested_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    source              TEXT NOT NULL DEFAULT 'manual',
    processed           BOOLEAN NOT NULL DEFAULT FALSE,
    processed_at        TIMESTAMPTZ,
    processing_error    TEXT
);

CREATE INDEX idx_hris_events_employee_occurred
    ON hris.employee_events(employee_no, occurred_at DESC);
CREATE INDEX idx_hris_events_processed_queue
    ON hris.employee_events(processed, ingested_at);

-- =====================================================================
-- 4. identity.users — add hris_employee_no FK column
-- =====================================================================
-- Replaces the unstructured employee_id label. The FK is NOT VALID at create
-- time (so we can validate it later after backfill); legacy rows can keep
-- employee_id and leave hris_employee_no NULL.
ALTER TABLE identity.users
    ADD COLUMN IF NOT EXISTS hris_employee_no TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_hris_employee_no
    ON identity.users(hris_employee_no)
    WHERE hris_employee_no IS NOT NULL;

-- Best-effort backfill: any existing employee_id that's purely uppercase
-- alphanumeric and 4-20 chars becomes the canonical employee_no. Anything
-- that doesn't fit the heuristic stays NULL — Admin will need to map them.
UPDATE identity.users
   SET hris_employee_no = employee_id
 WHERE hris_employee_no IS NULL
   AND employee_id IS NOT NULL
   AND employee_id ~ '^[A-Z0-9]{4,20}$';

-- NOT VALID FK — validated in a later wave after backfill is complete.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'fk_user_hris_employee'
    ) THEN
        ALTER TABLE identity.users
            ADD CONSTRAINT fk_user_hris_employee
            FOREIGN KEY (hris_employee_no)
            REFERENCES hris.employees(employee_no)
            NOT VALID;
    END IF;
END$$;

-- =====================================================================
-- 5. Permissions — additive grants for the HRIS surface
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('hris', 'employee.read',  'View HRIS employees'),
    ('hris', 'employee.write', 'Create / edit HRIS employees'),
    ('hris', 'event.read',     'View HRIS employee events'),
    ('hris', 'event.ingest',   'Ingest HRIS employee events'),
    ('hris', 'sync.run',       'Trigger a manual HRIS gateway sync')
ON CONFLICT DO NOTHING;

-- super_admin → all hris permissions
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r
CROSS JOIN identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'hris'
ON CONFLICT DO NOTHING;

-- hr_admin → read/write + ingest + sync (seed the role if missing)
INSERT INTO identity.roles (name, description) VALUES
    ('hr_admin', 'HR administrator — manages employee records and HRIS sync')
ON CONFLICT (name) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'hr_admin'
  AND p.module = 'hris'
  AND p.action IN ('employee.read','employee.write','event.read','event.ingest','sync.run')
ON CONFLICT DO NOTHING;

-- finance_admin → read-only (employee lookups for commission cessation review)
INSERT INTO identity.roles (name, description) VALUES
    ('finance_admin', 'Finance administrator — read-only HRIS access for commission review')
ON CONFLICT (name) DO NOTHING;

INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_admin'
  AND p.module = 'hris'
  AND p.action IN ('employee.read','event.read')
ON CONFLICT DO NOTHING;

-- =====================================================================
-- 6. Seed — 3 demo employees + 1 resigned event
-- =====================================================================
INSERT INTO hris.employees (employee_no, full_name, email, phone, department, position, hire_date, status, kyc_completed)
VALUES
    ('EMP00001', 'Andi Pratama',   'andi.pratama@ion.example',   '+62811000001', 'Sales',     'Sales Rep',        '2024-01-15', 'active',   TRUE),
    ('EMP00002', 'Bunga Lestari',  'bunga.lestari@ion.example',  '+62811000002', 'Operations','Team Lead',        '2023-06-01', 'active',   TRUE),
    ('EMP00003', 'Cipto Wijaya',   'cipto.wijaya@ion.example',   '+62811000003', 'Sales',     'Senior Sales Rep', '2022-04-12', 'resigned', TRUE)
ON CONFLICT (employee_no) DO NOTHING;

UPDATE hris.employees SET resign_date = DATE '2026-03-31' WHERE employee_no = 'EMP00003';

INSERT INTO hris.employee_events (employee_no, event_kind, event_payload, occurred_at, source, processed)
VALUES (
    'EMP00003',
    'resigned',
    '{"reason": "personal", "final_day": "2026-03-31"}'::jsonb,
    TIMESTAMPTZ '2026-03-31 23:59:59+07',
    'seed',
    FALSE
);
