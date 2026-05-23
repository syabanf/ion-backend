-- Wave 95 — drop Customer PO + Intercompany PO scaffolding.

BEGIN;

-- Permissions — remove grants first, then the permission rows.
DELETE FROM identity.role_permissions
WHERE permission_id IN (
    SELECT id FROM identity.permissions
    WHERE module = 'enterprise'
      AND action IN (
          'customer_po.read', 'customer_po.write',
          'intercompany_po.read', 'intercompany_po.write',
          'intercompany_po.accept', 'intercompany_po.reject',
          'intercompany_po.cancel'
      )
);

DELETE FROM identity.permissions
WHERE module = 'enterprise'
  AND action IN (
      'customer_po.read', 'customer_po.write',
      'intercompany_po.read', 'intercompany_po.write',
      'intercompany_po.accept', 'intercompany_po.reject',
      'intercompany_po.cancel'
  );

-- Triggers (dropped implicitly with the tables, but explicit is safer
-- if a follow-up migration only drops one of them).
DROP TRIGGER IF EXISTS trg_intercompany_pairs_touch ON enterprise.intercompany_pairs;
DROP TRIGGER IF EXISTS trg_intercompany_pos_touch ON enterprise.intercompany_pos;
DROP TRIGGER IF EXISTS trg_customer_pos_touch ON enterprise.customer_pos;

-- Tables — drop in reverse dependency order.
DROP TABLE IF EXISTS enterprise.intercompany_po_lines;
DROP TABLE IF EXISTS enterprise.intercompany_pos;
DROP TABLE IF EXISTS enterprise.customer_pos;
DROP TABLE IF EXISTS enterprise.intercompany_pairs;

COMMIT;
