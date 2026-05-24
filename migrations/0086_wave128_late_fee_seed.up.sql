-- Wave 128 — Late-fee policy seed (Gap 2 from SIT smoke).
--
-- Wave 121C's TestBillingCron_RunLateFeeTick was skipping because the
-- DEFAULT billing schema seeded by 0035 only carried the legacy-shape
-- late-fee keys (late_fee_amount + late_fee_grace_days). The orchestrator
-- in internal/billing/usecase/orchestration.go::resolveLateFeePolicy
-- accepts both shapes but prefers the PRD-shape nested `late_fee` block
-- when present:
--
--     body["late_fee"] = {
--         flat_amount, percentage_of_outstanding, cap_amount,
--         grace_days, disabled
--     }
--
-- This migration republishes the DEFAULT billing schema with version_no=2
-- that adds the nested `late_fee` block. Existing customers without an
-- override automatically pick it up at the next tick (Tier-4 DEFAULT
-- fallback in ResolveSchemaForCustomer). Legacy keys are retained so any
-- caller reading them via resolvedPolicyFor still gets the same numbers.
--
-- Idempotent: the ON CONFLICT clause covers the (kind, code, version_no)
-- triple; the supersede UPDATE is guarded by a WHERE on status='published'
-- so re-running this migration is a no-op once v2 is live.

BEGIN;

-- 1. Supersede the previous DEFAULT billing schema. We only flip status
--    when the body of v1 lacks the nested late_fee block; this lets the
--    migration co-exist with any environment where an operator has
--    already replaced v1 with a richer body.
UPDATE platform.schema_definitions
   SET status        = 'superseded',
       superseded_at = NOW()
 WHERE kind          = 'billing'
   AND code          = 'DEFAULT'
   AND status        = 'published'
   AND NOT (body ? 'late_fee');

-- 2. Insert v2 with the nested late_fee block. The flat/percentage/cap
--    numbers mirror the Wave 128 spec:
--       flat_amount               = 25 000 IDR
--       percentage_of_outstanding = 5%
--       cap_amount                = 100 000 IDR
--       grace_days                = 7
--    Percentage takes precedence over flat in domain.LateFeePolicy.Compute,
--    so a 111 000 IDR overdue invoice yields min(111000*0.05, 100000) =
--    5 550 IDR. Legacy fields are kept so older readers still resolve.
INSERT INTO platform.schema_definitions (
    kind, code, version_no, name, description, body, status, published_at
)
VALUES (
    'billing',
    'DEFAULT',
    2,
    'Default Billing Policy (Wave 128 — nested late_fee)',
    'Wave 128 supersede of the v1 DEFAULT billing schema. Adds the nested late_fee block so the billing-orchestration cron resolves a non-zero fee on overdue invoices for customers without a per-customer override.',
    jsonb_build_object(
        -- Nested PRD-shape — orchestration prefers this.
        'late_fee', jsonb_build_object(
            'flat_amount',               25000,
            'percentage_of_outstanding', 5,
            'cap_amount',                100000,
            'grace_days',                7,
            'disabled',                  false
        ),
        -- Legacy-shape mirrors (drop-in for older callers).
        'grace_days',                    7,
        'late_fee_pct',                  5,
        'suspend_after_days',            14,
        'reminder_days_before',          jsonb_build_array(7, 3, 1),
        'late_fee_grace_days',           7,
        'late_fee_amount',               25000,
        'terminate_after_suspended_days', 30,
        'notify_customer_days_before',   7
    ),
    'published',
    NOW()
)
ON CONFLICT (kind, code, version_no) DO NOTHING;

COMMIT;
