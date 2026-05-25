-- Wave 132 — broadband vs enterprise WO category.
--
-- Adds field.work_orders.wo_category so the tech app + dashboard can
-- visually distinguish broadband customer WOs from enterprise WOs at a
-- glance. The category lives on the WO itself (not derived at read
-- time) because:
--   1. A customer's segment can change over time; the WO snapshot must
--      reflect the segment AT THE TIME of dispatch so a tech doesn't
--      see "enterprise" on an old install that was sold as broadband.
--   2. Termination WOs may outlive the customer record (auto-suspend
--      flow can delete the customer); the WO needs to carry its own
--      category.
--   3. Filter queries on category-only avoid a costly customer JOIN.
--
-- Allowed values: 'broadband', 'enterprise'.
--
-- Mapping from existing crm.customers.customer_type:
--   broadband               → broadband
--   business / enterprise / corporate → enterprise
--
-- Default 'broadband' for any orphan rows (legacy data without a
-- linked customer). NOT NULL so the application never has to deal
-- with three-valued logic.

ALTER TABLE field.work_orders
    ADD COLUMN wo_category text NOT NULL DEFAULT 'broadband'
        CHECK (wo_category IN ('broadband', 'enterprise'));

-- Backfill from the linked customer's customer_type. LEFT JOIN
-- intentionally — rows with no matching customer keep the default.
UPDATE field.work_orders w
   SET wo_category = CASE
       WHEN c.customer_type IN ('business', 'enterprise', 'corporate') THEN 'enterprise'
       ELSE 'broadband'
   END
  FROM crm.customers c
 WHERE c.id = w.customer_id;

-- Filter index — the WO list page filters by category for the
-- "enterprise techs only" use case (their queue should not include
-- broadband installs). Partial-index on the rare value would be
-- smaller, but tech queue queries always include 'enterprise' so a
-- plain btree wins.
CREATE INDEX IF NOT EXISTS work_orders_category_idx
    ON field.work_orders(wo_category);
