-- 0051 — Wave 78: Customer schema version lock
--
-- Closes TC-SCH-011 ("customer at v1.0 stays on v1.0 when v1.1 published"),
-- TC-SCH-015 ("new customer auto-receives default schema set"),
-- TC-SCH-023 ("runtime confirms version-lock"), TC-SCH-026 ("locked version
-- reflects order-time schema, not current product schema"), TC-PRD-025
-- ("product schema change NOT retroactive").
--
-- Today the resolver picks `FindLatestPublished` every time. If a customer
-- was activated under v1.0 of the broadband-billing schema and the admin
-- publishes v1.1, the next dunning tick reads v1.1's late-fee rules —
-- silently retro-changing the customer's contract. QA correctly flagged
-- this as the "version lock" gap.
--
-- The fix: at the moment a customer is created from lead conversion, the
-- resolver snapshots the *version_id* of every kind it resolved and pins
-- the customer to those specific versions. Subsequent reads prefer the
-- locked version over `FindLatestPublished`.
--
-- All 5 columns are nullable so existing customers (created before this
-- migration) gracefully fall through to the existing resolver path —
-- they only get locked when someone explicitly migrates them via the
-- bulk-migration tool (TC-SCH-013 / Wave 79).

BEGIN;

ALTER TABLE crm.customers
    ADD COLUMN IF NOT EXISTS locked_onboarding_schema_version_id  UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS locked_billing_schema_version_id     UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS locked_service_schema_version_id     UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS locked_commission_schema_version_id  UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS locked_suspension_schema_version_id  UUID
        REFERENCES platform.schema_definitions(id) ON DELETE SET NULL;

-- The resolver visits these columns on every dunning tick / portal hit
-- per customer, so they're worth indexing for occasional reverse lookups
-- ("how many customers are locked to this schema version?").
CREATE INDEX IF NOT EXISTS idx_customer_locked_onboarding
    ON crm.customers (locked_onboarding_schema_version_id)
    WHERE locked_onboarding_schema_version_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_customer_locked_billing
    ON crm.customers (locked_billing_schema_version_id)
    WHERE locked_billing_schema_version_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_customer_locked_service
    ON crm.customers (locked_service_schema_version_id)
    WHERE locked_service_schema_version_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_customer_locked_commission
    ON crm.customers (locked_commission_schema_version_id)
    WHERE locked_commission_schema_version_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_customer_locked_suspension
    ON crm.customers (locked_suspension_schema_version_id)
    WHERE locked_suspension_schema_version_id IS NOT NULL;

COMMIT;
