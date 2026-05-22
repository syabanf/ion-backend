-- 0050 — Wave 77: Product↔schema FK foundation
--
-- PRD §1 ("New customer types or product lines can be introduced by
-- creating new schema sets") + QA TC-PRD-014/016/018/019/022/023/024/
-- 025/026 + TC-WO-011 require that a broadband Plan can carry its own
-- per-kind schema assignments. The current shape has `crm.products` as
-- a flat 9-column table with no schema linkage; the resolver in
-- `internal/platform/usecase/service.go` falls back from a customer
-- override straight to the global DEFAULT code for the kind.
--
-- This migration adds the 5 schema FK columns. They're nullable + ON
-- DELETE SET NULL so deactivating a schema doesn't orphan the product
-- (the resolver falls through to the customer-type default — see
-- Wave 77's `resolveProductSchema` change for the new lookup order).
--
-- Closes the *schema* portion of the cluster. The audit-on-update path
-- (TC-PRD-013/028) lands in Wave 81.

BEGIN;

ALTER TABLE crm.products
    ADD COLUMN IF NOT EXISTS onboarding_schema_id  UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS billing_schema_id     UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS service_schema_id     UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS commission_schema_id  UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS suspension_schema_id  UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL;

-- Indexes — these FKs are scanned during resolver runs (lots of
-- "find product → resolve schema" hits at WO creation time).
CREATE INDEX IF NOT EXISTS idx_crm_products_onboarding_schema
    ON crm.products (onboarding_schema_id)
    WHERE onboarding_schema_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_crm_products_service_schema
    ON crm.products (service_schema_id)
    WHERE service_schema_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_crm_products_billing_schema
    ON crm.products (billing_schema_id)
    WHERE billing_schema_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_crm_products_commission_schema
    ON crm.products (commission_schema_id)
    WHERE commission_schema_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_crm_products_suspension_schema
    ON crm.products (suspension_schema_id)
    WHERE suspension_schema_id IS NOT NULL;

-- Note on TC-PRD-023 (required schema slot enforcement):
--   The PRD says "products cannot save as Active if a required schema
--   slot is empty AND no customer-type default exists". We don't add
--   a CHECK here because the constraint is conditional on the
--   *customer-type* default schema existing — a multi-table predicate
--   PostgreSQL CHECK can't express. The enforcement lives in the
--   domain layer (`product.go::SaveActive`) where we have the resolver.

COMMIT;
