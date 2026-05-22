-- 0047 — Master data seed (down).
--
-- Removes ONLY the discrete rows this migration inserted that have no
-- overlap with other migrations:
--
--   * crm.products             (BB-10, BB-30, BB-50, BB-100)
--   * identity.branches        (HQ — only if no user references it)
--   * enterprise.approval_templates       (APT-BOQ-DEFAULT, APT-RFQ-DEFAULT)
--   * enterprise.ewo_checklist_templates  (EWO-INSTALL-STD, EWO-MAINT-STD)
--
-- We INTENTIONALLY do not roll back identity.permissions /
-- identity.role_permissions because earlier migrations (0002, 0006, 0007,
-- 0008, 0009, 0011, 0014, 0024, 0026, 0028, 0029, 0030, 0032, 0033, 0036,
-- 0038, 0040, 0041, 0044) also seed those tables with overlapping keys
-- (the dashboard's `Can permission=` strings are stable across migrations
-- and many already-shipped migrations top them up). A blanket DELETE here
-- would tear out permissions other migrations rely on, and the
-- ON CONFLICT DO NOTHING in those earlier migrations means they would
-- NOT be re-inserted on a `migrate up` redo. The safest invariant: this
-- migration's down is best-effort cleanup of *new* leaf rows only.
--
-- If an operator genuinely needs to rebuild the permissions catalog from
-- scratch, the recommended path is `DROP DATABASE` + re-migrate (CI
-- migration smoke covers this). Hand-rolled rollback of RBAC catalog is
-- not supported.

BEGIN;

DELETE FROM enterprise.ewo_checklist_templates
WHERE code IN ('EWO-INSTALL-STD', 'EWO-MAINT-STD');

DELETE FROM enterprise.approval_templates
WHERE key IN ('APT-BOQ-DEFAULT', 'APT-RFQ-DEFAULT');

DELETE FROM crm.products
WHERE code IN ('BB-10', 'BB-30', 'BB-50', 'BB-100');

-- HQ branch — only when nothing depends on it. We check users (FK SET NULL
-- but operators expect HQ to vanish only when truly orphaned).
DELETE FROM identity.branches
WHERE code = 'HQ'
  AND NOT EXISTS (
    SELECT 1 FROM identity.users u WHERE u.branch_id = identity.branches.id
  );

COMMIT;
