-- Wave 85 (Tier 3 starter) — first-class purchase orders.
--
-- warehouse.assets has carried a free-form `purchase_order_ref TEXT`
-- since migration 0006, but Phase 1B (TC-WHS-PO-001..014) wants a
-- proper PO surface: a header row with branch + supplier + status,
-- lines specifying which stock items to receive and how many, and a
-- distinct goods-receipt workflow that lands intake stock + asset
-- rows against the PO when shipment arrives.
--
-- Scope of THIS migration: PO header + lines only. The
-- goods_receipt workflow lands in Wave 86 — it depends on this
-- structure but warrants its own migration + integration tests.
--
-- Status flow:
--   draft → submitted → approved → receiving → closed
--                                        ↘ cancelled
-- All transitions are explicit POSTs in the usecase; the DB CHECK
-- enforces the enum so the dashboard can't write a bad status by
-- accident.

CREATE TABLE warehouse.purchase_orders (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- PO-YYYYMMDD-XXXX, generated at usecase time.
    po_number           TEXT NOT NULL UNIQUE,
    supplier_id         UUID NOT NULL REFERENCES warehouse.suppliers(id) ON DELETE RESTRICT,
    branch_id           UUID NOT NULL REFERENCES identity.branches(id) ON DELETE RESTRICT,
    -- The warehouse the goods will land in. Required so the GR step
    -- knows where to credit the intake.
    receiving_warehouse_id UUID NOT NULL REFERENCES warehouse.warehouses(id) ON DELETE RESTRICT,
    status              TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','submitted','approved','receiving','closed','cancelled')),
    -- Snapshot the totals at the row so reporting doesn't have to
    -- re-aggregate from lines. Updated whenever lines change.
    subtotal            NUMERIC(14, 2) NOT NULL DEFAULT 0,
    ppn_rate            NUMERIC(5, 2) NOT NULL DEFAULT 11.00,
    total               NUMERIC(14, 2) NOT NULL DEFAULT 0,
    expected_at         DATE,
    notes               TEXT,
    created_by          UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    submitted_by        UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    submitted_at        TIMESTAMPTZ,
    approved_by         UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    approved_at         TIMESTAMPTZ,
    closed_at           TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    cancelled_reason    TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_po_branch     ON warehouse.purchase_orders (branch_id);
CREATE INDEX idx_po_supplier   ON warehouse.purchase_orders (supplier_id);
CREATE INDEX idx_po_status     ON warehouse.purchase_orders (status);
CREATE INDEX idx_po_created_at ON warehouse.purchase_orders (created_at DESC);

CREATE TABLE warehouse.purchase_order_lines (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    purchase_order_id   UUID NOT NULL REFERENCES warehouse.purchase_orders(id) ON DELETE CASCADE,
    line_no             INT NOT NULL,
    stock_item_id       UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    -- For serialized items quantity is the count of serials expected.
    -- For cable / consumables it's meters / units. The unit matches
    -- the stock_item's unit (validated in the usecase).
    quantity_ordered    NUMERIC(18, 3) NOT NULL CHECK (quantity_ordered > 0),
    -- Set as goods are received via the Wave 86 GR workflow; bumped
    -- atomically with the GR insert so a PO line never shows a
    -- received value without a corresponding GR row.
    quantity_received   NUMERIC(18, 3) NOT NULL DEFAULT 0,
    unit_cost           NUMERIC(14, 2) NOT NULL CHECK (unit_cost >= 0),
    -- subtotal = quantity_ordered * unit_cost, snapshotted so reporting
    -- doesn't have to multiply in queries.
    line_subtotal       NUMERIC(14, 2) NOT NULL DEFAULT 0,
    notes               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (purchase_order_id, line_no)
);

CREATE INDEX idx_po_lines_stock_item ON warehouse.purchase_order_lines (stock_item_id);

-- Foreign key from assets back to the originating PO (supersedes the
-- free-form purchase_order_ref TEXT column; we keep that column for
-- legacy rows until the Wave 86 GR workflow lands and starts writing
-- structured FKs on intake).
ALTER TABLE warehouse.assets
    ADD COLUMN IF NOT EXISTS purchase_order_id UUID
        REFERENCES warehouse.purchase_orders(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_assets_purchase_order
    ON warehouse.assets (purchase_order_id) WHERE purchase_order_id IS NOT NULL;
