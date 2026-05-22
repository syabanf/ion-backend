-- M6 r4 — Seed DEFAULT-code published schemas for the four
-- platform.schema_kind values.
--
-- Why: the Resolver consumed by billing-svc + crm-svc needs at least
-- one published schema per kind to fall back to when no customer
-- override exists. Without these rows the resolver returns NotFound on
-- every tick — billing/commission/suspension code then transparently
-- falls back to the legacy billing.policies row (kept as a safety
-- hatch), but the schema-driven path never actually exercises. This
-- migration unlocks the runtime usage of Schema System v1.
--
-- Values mirror what the runtime previously hardcoded:
--   * billing:    billing.policies row defaults (grace_days=3,
--                 suspend_after_days=14, late_fee_amount=25000,
--                 reminder_days_before=[7,3,1] — we expand the
--                 single notify_customer_days_before=7 into the
--                 PRD-shape "7 / 3 / 1 days before due_date" reminder
--                 ladder so the schema is forward-compatible with the
--                 messaging tick that lands in r5).
--   * commission: billing/domain.DefaultCommissionPercents (15/5/10/10/60).
--                 Stored as fractions in the schema (0.15/0.05/...) so
--                 the body matches the keys the PRD uses. The billing
--                 code accepts both percent (>1) and fraction (<=1)
--                 shapes — see commissionPct helper.
--   * suspension: billing.policies.suspend_after_days +
--                 grace_minutes_after_suspend=60 (matches PRD
--                 "60-minute soft-disconnect window" before RADIUS
--                 hard-disable).
--   * service:    placeholder — no consumer yet (round-5 service
--                 catalog work will hang capacity rules off this).
--
-- Idempotent via ON CONFLICT (kind, code, version_no) DO NOTHING — the
-- UNIQUE constraint shipped in 0032 protects us from double-seeding on
-- re-apply.

INSERT INTO platform.schema_definitions (
    kind, code, version_no, name, description, body, status, published_at
)
VALUES
    (
        'billing', 'DEFAULT', 1,
        'Default Billing Policy',
        'Baseline billing schema — applied to every customer without a customer-level billing override. Mirrors the legacy billing.policies row so resolver fallback is a no-op.',
        jsonb_build_object(
            -- New schema-driven names (PRD-shape).
            'grace_days',             3,
            'late_fee_pct',           0,
            'suspend_after_days',     14,
            'reminder_days_before',   jsonb_build_array(7, 3, 1),
            -- Legacy billing.policies-aligned names so the resolver
            -- result is a drop-in replacement for the policy struct.
            'late_fee_grace_days',          3,
            'late_fee_amount',              25000,
            'terminate_after_suspended_days', 30,
            'notify_customer_days_before',  7
        ),
        'published',
        NOW()
    ),
    (
        'commission', 'DEFAULT', 1,
        'Default Commission Split',
        '5-party commission split applied to the monthly_price of an order on first-OTC-paid. Fractions sum to 1.0. Cross-branch detection at runtime folds branch_infra into holding when same-branch.',
        jsonb_build_object(
            -- Fraction-shape (PRD).
            'rep_pct',           0.15,
            'mgr_pct',           0.05,
            'branch_sales_pct',  0.10,
            'branch_infra_pct',  0.10,
            'holding_pct',       0.60
        ),
        'published',
        NOW()
    ),
    (
        'suspension', 'DEFAULT', 1,
        'Default Suspension Policy',
        'Auto-suspension thresholds applied when an invoice is unpaid past suspend_after_days. grace_minutes_after_suspend buys the customer a soft-disconnect window before RADIUS hard-disable.',
        jsonb_build_object(
            'suspend_after_days',          14,
            'grace_minutes_after_suspend', 60,
            -- Mirror billing-side terminate threshold so the
            -- suspension tick can read it without joining back to
            -- billing.policies.
            'terminate_after_suspended_days', 30
        ),
        'published',
        NOW()
    ),
    (
        'service', 'DEFAULT', 1,
        'Default Service Profile',
        'Placeholder — no consumer wired yet. Round-5 service catalog work will hang capacity / FUP rules here.',
        '{}'::jsonb,
        'published',
        NOW()
    )
ON CONFLICT (kind, code, version_no) DO NOTHING;
