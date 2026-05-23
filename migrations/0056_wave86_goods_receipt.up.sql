-- Wave 86 (Tier 3) — Goods Receipt workflow.
--
-- Depends on Wave 85's purchase_orders + purchase_order_lines. Each
-- receipt event is a header + lines that lists what physically
-- arrived. The CreateReceipt usecase runs the whole thing in one tx
-- so quantity_received on the parent PO lines stays in lock-step with
-- the receipt rows.
--
-- Status flow on the PO itself (Wave 85 added the column; this
-- migration starts using it):
--
--   approved → receiving on first receipt
--   receiving → closed when all lines reach quantity_received >= ordered

CREATE TABLE warehouse.goods_receipts (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- GR-YYYYMMDD-XXXX, generated server-side. Same pattern as PO.
    receipt_number      TEXT NOT NULL UNIQUE,
    purchase_order_id   UUID NOT NULL REFERENCES warehouse.purchase_orders(id) ON DELETE RESTRICT,
    warehouse_id        UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    received_by         UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    -- Optional carrier reference / waybill so the audit trail can
    -- reconcile against the supplier shipment without joining out.
    carrier_ref         TEXT,
    notes               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_gr_po          ON warehouse.goods_receipts (purchase_order_id);
CREATE INDEX idx_gr_warehouse   ON warehouse.goods_receipts (warehouse_id);
CREATE INDEX idx_gr_received_at ON warehouse.goods_receipts (received_at DESC);

CREATE TABLE warehouse.goods_receipt_lines (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    goods_receipt_id        UUID NOT NULL REFERENCES warehouse.goods_receipts(id) ON DELETE CASCADE,
    purchase_order_line_id  UUID NOT NULL REFERENCES warehouse.purchase_order_lines(id) ON DELETE RESTRICT,
    quantity_received       NUMERIC(18, 3) NOT NULL CHECK (quantity_received > 0),
    -- Snapshot the unit cost actually paid; usually matches the PO
    -- line but suppliers sometimes invoice slightly different rates.
    unit_cost               NUMERIC(14, 2) NOT NULL CHECK (unit_cost >= 0),
    -- For serialized items the asset_id is set per receipt line
    -- (one asset per serial). For non-serialized (cable / consumables)
    -- the line stays flat and the quantity covers the bulk.
    --
    -- We DON'T enforce 1:1 (asset_id ↔ serialized) at DB level because
    -- some catalog stock items are "serialized but receipt-aggregated"
    -- in edge cases; the usecase enforces the right invariant per
    -- stock_item.serialized flag.
    asset_id                UUID REFERENCES warehouse.assets(id) ON DELETE SET NULL,
    notes                   TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_grl_receipt ON warehouse.goods_receipt_lines (goods_receipt_id);
CREATE INDEX idx_grl_po_line ON warehouse.goods_receipt_lines (purchase_order_line_id);
CREATE INDEX idx_grl_asset   ON warehouse.goods_receipt_lines (asset_id) WHERE asset_id IS NOT NULL;
