-- 0034_broadband_happy_path — Gap B from the broadband happy-path triage.
--
-- Adds crm.orders.otc_type so the billing OTC invoice generator can dispatch
-- on the three PRD-defined paths:
--
--   free      — no invoice, contract is contracted immediately
--   prepaid   — invoice issued at conversion, contract activates on payment
--   postpaid  — invoice deferred until activation (existing behaviour);
--               this is the round-1 default for back-compat
--
-- The default is `postpaid` because that's what every order persisted before
-- this migration ran was implicitly doing. Promo/free-install flows can
-- override per-order at conversion time once the product catalogue starts
-- carrying its own otc_type.

ALTER TABLE crm.orders
    ADD COLUMN otc_type TEXT NOT NULL DEFAULT 'postpaid'
        CHECK (otc_type IN ('free', 'prepaid', 'postpaid'));

CREATE INDEX idx_crm_orders_otc_type ON crm.orders (otc_type);
