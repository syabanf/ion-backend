-- Wave 87 (Tier 3) — Asset Retrofit log.
--
-- PRD §8A: when an end-of-life asset still has serviceable parts,
-- those parts are harvested into a new asset. The source asset goes
-- to 'cannibalized' status and the produced asset is created with
-- `is_retrofit=true`. The pair of movements (retrofit_consume +
-- retrofit_produce) already lives in warehouse.stock_movements
-- (migration 0006). This migration adds the explicit audit log so
-- "what produced this asset" / "what did this cannibalized asset
-- become" can be answered in one query.

CREATE TABLE warehouse.asset_retrofits (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    source_asset_id     UUID NOT NULL REFERENCES warehouse.assets(id) ON DELETE RESTRICT,
    produced_asset_id   UUID NOT NULL REFERENCES warehouse.assets(id) ON DELETE RESTRICT,
    reason              TEXT NOT NULL,
    performed_by        UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    performed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Both stock_movement rows that pair with this retrofit. Filled in
    -- by the same tx that creates the rows in warehouse.stock_movements
    -- (movement_type='retrofit_consume' + 'retrofit_produce').
    consume_movement_id UUID REFERENCES warehouse.stock_movements(id) ON DELETE SET NULL,
    produce_movement_id UUID REFERENCES warehouse.stock_movements(id) ON DELETE SET NULL,
    UNIQUE (source_asset_id, produced_asset_id)
);

CREATE INDEX idx_retrofit_source   ON warehouse.asset_retrofits (source_asset_id);
CREATE INDEX idx_retrofit_produced ON warehouse.asset_retrofits (produced_asset_id);
CREATE INDEX idx_retrofit_when     ON warehouse.asset_retrofits (performed_at DESC);
