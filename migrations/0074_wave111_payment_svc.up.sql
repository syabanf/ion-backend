-- Wave 111 — Payment Service bounded context.
--
-- Phase 1B Broadband UAT requires a dedicated payment microservice
-- handling routing, webhook ingestion, H2H bank integration, and the
-- refund flow. This migration creates the `payment` schema and the six
-- aggregate tables it owns:
--
--   - payment.payment_gateways         — gateway registry (Xendit, BCA H2H, …)
--   - payment.payment_methods          — saved customer payment methods
--   - payment.payment_intents          — one row per checkout attempt
--   - payment.payment_webhooks         — verified inbound webhook events
--   - payment.refunds                  — refund requests + lifecycle
--   - payment.h2h_bank_statements      — uploaded bank statement files
--   - payment.h2h_bank_lines           — parsed individual line items
--
-- Permissions added (module='payment'):
--   - intent.read / intent.write
--   - refund.read / refund.write / refund.approve
--   - webhook.read
--   - gateway.read / gateway.write
--   - h2h.read / h2h.upload / h2h.match
--
-- Role grants:
--   - super_admin                    → every permission
--   - finance_admin (created here)   → intent.* + refund.* + h2h.* + webhook.read + gateway.read
--   - finance_viewer (created here)  → all .read permissions only

BEGIN;

CREATE SCHEMA IF NOT EXISTS payment;

-- =====================================================================
-- payment.payment_gateways — gateway registry.
--
-- One row per integrated payment processor. `kind` partitions the
-- routing strategy (VA aggregators get routed by amount tier; H2H
-- banks get routed by direct corporate relationships). `priority` is
-- the routing tie-breaker (lower = preferred). `config_encrypted`
-- holds the gateway's API credentials sealed via pkg/cryptutil so an
-- accidental SELECT * doesn't leak secrets.
-- =====================================================================
CREATE TABLE payment.payment_gateways (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code                 TEXT NOT NULL UNIQUE,
    name                 TEXT NOT NULL,
    kind                 TEXT NOT NULL
        CHECK (kind IN ('va_bank', 'va_aggregator', 'ewallet', 'qris',
                        'h2h_bank', 'card', 'crypto')),
    is_active            BOOLEAN NOT NULL DEFAULT TRUE,
    priority             INT NOT NULL DEFAULT 100,
    supported_methods    JSONB NOT NULL DEFAULT '[]'::jsonb,
    min_amount           NUMERIC(18,2),
    max_amount           NUMERIC(18,2),
    config_encrypted     BYTEA,
    config_key_version   INT NOT NULL DEFAULT 1,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_gateways_active_priority
    ON payment.payment_gateways (is_active, priority);

-- Seed: Xendit (VA aggregator, top priority), BCA H2H (corporate bank),
-- Midtrans (VA aggregator fallback), Stripe-stub (card, inactive until
-- the international expansion lands).
INSERT INTO payment.payment_gateways
    (id, code, name, kind, is_active, priority, supported_methods)
VALUES
    ('00000000-0000-0000-0000-0000000a0001'::uuid,
     'xendit', 'Xendit', 'va_aggregator',
     TRUE, 10,
     '["va_bca", "va_bni", "va_bri", "va_mandiri", "ewallet_ovo", "ewallet_dana", "ewallet_gopay", "qris", "retail_indomaret", "retail_alfamart"]'::jsonb),
    ('00000000-0000-0000-0000-0000000a0002'::uuid,
     'bca_h2h', 'BCA Host-to-Host', 'h2h_bank',
     TRUE, 20,
     '["va_bca_corporate", "rtgs", "skn"]'::jsonb),
    ('00000000-0000-0000-0000-0000000a0003'::uuid,
     'midtrans', 'Midtrans', 'va_aggregator',
     TRUE, 30,
     '["va_bca", "va_bni", "va_bri", "va_mandiri", "ewallet_gopay", "credit_card"]'::jsonb),
    ('00000000-0000-0000-0000-0000000a0004'::uuid,
     'stripe', 'Stripe (stub)', 'card',
     FALSE, 99,
     '["credit_card", "debit_card"]'::jsonb)
ON CONFLICT (id) DO NOTHING;

-- =====================================================================
-- payment.payment_methods — saved customer payment instruments.
--
-- Used by Wave 111+ self-service: customer saves a default VA / card
-- token so subsequent invoices can short-circuit method selection.
-- `masked_account` keeps a display-safe last-4 (never raw PAN).
-- =====================================================================
CREATE TABLE payment.payment_methods (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id       UUID NOT NULL,
    kind              TEXT NOT NULL,
    gateway_id        UUID NOT NULL
        REFERENCES payment.payment_gateways(id),
    masked_account    TEXT,
    expires_at        TIMESTAMPTZ,
    is_default        BOOLEAN NOT NULL DEFAULT FALSE,
    last_used_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_methods_customer_default
    ON payment.payment_methods (customer_id, is_default);

CREATE INDEX idx_payment_methods_gateway_used
    ON payment.payment_methods (gateway_id, last_used_at DESC);

-- =====================================================================
-- payment.payment_intents — one row per checkout attempt.
--
-- State machine (enforced in Go domain, checked by DB CHECK):
--   created → routing → pending → succeeded
--                              ↘ failed / expired / cancelled
--   succeeded → partially_refunded → refunded
--
-- `idempotency_key` UNIQUE allows the create endpoint to be safely
-- retried by clients; the INSERT … ON CONFLICT DO NOTHING + re-fetch
-- pattern returns the originally-issued row on replay.
-- `routing_decision` snapshots the routing audit: which gateways were
-- considered, why the chosen one won, gateway scores. Read on demand
-- by finance support tooling.
-- =====================================================================
CREATE TABLE payment.payment_intents (
    id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    invoice_id               UUID NOT NULL,
    customer_id              UUID,
    gateway_id               UUID
        REFERENCES payment.payment_gateways(id),
    amount                   NUMERIC(18,2) NOT NULL,
    currency                 TEXT NOT NULL DEFAULT 'IDR',
    status                   TEXT NOT NULL DEFAULT 'created'
        CHECK (status IN ('created', 'routing', 'pending', 'succeeded',
                          'failed', 'expired', 'cancelled',
                          'refunded', 'partially_refunded')),
    routing_decision         JSONB,
    idempotency_key          TEXT UNIQUE,
    external_payment_ref     TEXT,
    paid_at                  TIMESTAMPTZ,
    expired_at               TIMESTAMPTZ,
    cancelled_at             TIMESTAMPTZ,
    failure_code             TEXT,
    failure_reason           TEXT,
    refunded_amount          NUMERIC(18,2) NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_intents_invoice_status
    ON payment.payment_intents (invoice_id, status);

CREATE INDEX idx_payment_intents_status_created
    ON payment.payment_intents (status, created_at DESC);

CREATE INDEX idx_payment_intents_external_ref
    ON payment.payment_intents (external_payment_ref)
    WHERE external_payment_ref IS NOT NULL;

-- =====================================================================
-- payment.payment_webhooks — verified inbound webhook events.
--
-- `(gateway_id, external_event_id)` UNIQUE is the dedup guard — a
-- redelivery from the gateway lands here as ON CONFLICT DO NOTHING and
-- the handler short-circuits with status='duplicate' rather than
-- replaying side effects on the intent. Signature verification happens
-- in the adapter (pkg/webhookx); a verification failure stores the
-- payload with signature_valid=false + status='suspect' so the SRE
-- runbook can investigate replay attempts.
-- =====================================================================
CREATE TABLE payment.payment_webhooks (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    gateway_id          UUID NOT NULL
        REFERENCES payment.payment_gateways(id),
    external_event_id   TEXT NOT NULL,
    payload             JSONB NOT NULL,
    signature_valid     BOOLEAN NOT NULL DEFAULT FALSE,
    status              TEXT NOT NULL DEFAULT 'received'
        CHECK (status IN ('received', 'verified', 'processed',
                          'failed', 'duplicate', 'suspect')),
    payment_intent_id   UUID,
    error_msg           TEXT,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_payment_webhooks_dedup
    ON payment.payment_webhooks (gateway_id, external_event_id);

CREATE INDEX idx_payment_webhooks_status_received
    ON payment.payment_webhooks (status, received_at DESC);

-- =====================================================================
-- payment.refunds — refund requests + lifecycle.
--
-- One row per refund attempt. The intent's `refunded_amount` is the
-- cumulative authoritative total (the usecase recomputes it from
-- completed refund rows on each transition). When `refunded_amount`
-- equals the intent's `amount` the intent flips to 'refunded';
-- otherwise it sits in 'partially_refunded'.
--
-- `ON DELETE RESTRICT` on the parent intent ensures financial history
-- can never be silently dropped by a CASCADE further upstream.
-- =====================================================================
CREATE TABLE payment.refunds (
    id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    payment_intent_id     UUID NOT NULL
        REFERENCES payment.payment_intents(id) ON DELETE RESTRICT,
    amount                NUMERIC(18,2) NOT NULL,
    reason                TEXT,
    status                TEXT NOT NULL DEFAULT 'requested'
        CHECK (status IN ('requested', 'approved', 'processing',
                          'completed', 'rejected', 'failed')),
    external_refund_ref   TEXT,
    requested_by          UUID,
    approved_by           UUID,
    approved_at           TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_refunds_intent
    ON payment.refunds (payment_intent_id);

CREATE INDEX idx_payment_refunds_status_created
    ON payment.refunds (status, created_at DESC);

-- =====================================================================
-- payment.h2h_bank_statements — uploaded H2H bank statement files.
--
-- `(gateway_id, raw_hash)` UNIQUE makes re-uploading the same file
-- idempotent — the finance team can drop a CSV twice without doubling
-- the matched lines. `matched_count` / `unmatched_count` are summary
-- counters maintained by the MatchStatement run.
-- =====================================================================
CREATE TABLE payment.h2h_bank_statements (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    gateway_id      UUID NOT NULL
        REFERENCES payment.payment_gateways(id),
    statement_date  DATE,
    raw_filename    TEXT NOT NULL,
    raw_hash        TEXT NOT NULL,
    line_count      INT NOT NULL DEFAULT 0,
    matched_count   INT NOT NULL DEFAULT 0,
    unmatched_count INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'parsing'
        CHECK (status IN ('parsing', 'parsed', 'matching',
                          'matched', 'partial', 'failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_h2h_statements_dedup
    ON payment.h2h_bank_statements (gateway_id, raw_hash);

-- =====================================================================
-- payment.h2h_bank_lines — parsed bank statement rows.
--
-- One row per CSV line. The matcher fuzzy-matches each line against
-- unmatched payment_intents using (reference_text + amount + ±2-day
-- window) and records the confidence score. Lines with confidence < a
-- configured threshold stay unmatched so finance can review them by
-- hand.
-- =====================================================================
CREATE TABLE payment.h2h_bank_lines (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    statement_id        UUID NOT NULL
        REFERENCES payment.h2h_bank_statements(id) ON DELETE CASCADE,
    raw_line            JSONB NOT NULL,
    amount              NUMERIC(18,2),
    value_date          DATE,
    reference_text      TEXT,
    payment_intent_id   UUID,
    match_confidence    NUMERIC(3,2),
    match_method        TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_h2h_lines_statement
    ON payment.h2h_bank_lines (statement_id);

CREATE INDEX idx_h2h_lines_intent
    ON payment.h2h_bank_lines (payment_intent_id)
    WHERE payment_intent_id IS NOT NULL;

CREATE INDEX idx_h2h_lines_reference
    ON payment.h2h_bank_lines (reference_text);

-- =====================================================================
-- Permissions + role grants.
-- =====================================================================
INSERT INTO identity.permissions (module, action, description) VALUES
    ('payment', 'intent.read',    'View payment intents'),
    ('payment', 'intent.write',   'Create / cancel payment intents'),
    ('payment', 'refund.read',    'View refund requests'),
    ('payment', 'refund.write',   'Request refunds'),
    ('payment', 'refund.approve', 'Approve / reject refund requests'),
    ('payment', 'webhook.read',   'View inbound webhook history'),
    ('payment', 'gateway.read',   'View payment gateway registry'),
    ('payment', 'gateway.write',  'Manage payment gateway registry'),
    ('payment', 'h2h.read',       'View H2H bank statements'),
    ('payment', 'h2h.upload',     'Upload H2H bank statements'),
    ('payment', 'h2h.match',      'Run / re-run H2H matching')
ON CONFLICT (module, action) DO NOTHING;

-- super_admin gets every payment permission.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'super_admin'
  AND p.module = 'payment'
ON CONFLICT DO NOTHING;

-- Create finance_admin + finance_viewer roles (idempotent).
INSERT INTO identity.roles (name, description) VALUES
    ('finance_admin',  'Finance administrator — manages payments, refunds, H2H reconciliation'),
    ('finance_viewer', 'Finance viewer — read-only access to payment + refund + H2H records')
ON CONFLICT (name) DO NOTHING;

-- finance_admin gets intent.* + refund.* + h2h.* + webhook.read + gateway.read.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_admin'
  AND p.module = 'payment'
  AND p.action IN (
      'intent.read', 'intent.write',
      'refund.read', 'refund.write', 'refund.approve',
      'webhook.read',
      'gateway.read',
      'h2h.read', 'h2h.upload', 'h2h.match'
  )
ON CONFLICT DO NOTHING;

-- finance_viewer gets read-only access.
INSERT INTO identity.role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM identity.roles r, identity.permissions p
WHERE r.name = 'finance_viewer'
  AND p.module = 'payment'
  AND p.action IN (
      'intent.read', 'refund.read', 'webhook.read',
      'gateway.read', 'h2h.read'
  )
ON CONFLICT DO NOTHING;

COMMIT;
