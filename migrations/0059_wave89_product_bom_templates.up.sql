-- Wave 89 (Tier 3) — Product BOM templates.
--
-- A WO dispatch (warehouse.wo_dispatches + .wo_dispatch_items)
-- already represents the bill-of-materials for a specific WO. Today
-- those items are built ad-hoc per dispatch — the operator has to
-- remember which stock_items every product needs.
--
-- This migration adds the **template** layer keyed by crm.products,
-- so a typical broadband install has a known set of items + default
-- quantities. The dispatch creation flow (Wave 89b) pre-fills from
-- the template, then operators can adjust per-WO before committing.
--
-- Templates are versioned by replacement rather than mutation — when
-- the catalog changes, deactivate the old template and create a new
-- one. This keeps the dispatch's historical link to "what BOM was
-- standard at the time" intact.

CREATE TABLE warehouse.product_bom_templates (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    product_id      UUID NOT NULL REFERENCES crm.products(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_by      UUID REFERENCES identity.users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Only one active template per product. Inactive templates linger so
-- historical dispatches can still reference them via the FK below.
CREATE UNIQUE INDEX idx_product_bom_active_one
    ON warehouse.product_bom_templates (product_id)
    WHERE active = TRUE;

CREATE TABLE warehouse.product_bom_template_items (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    template_id         UUID NOT NULL REFERENCES warehouse.product_bom_templates(id) ON DELETE CASCADE,
    stock_item_id       UUID NOT NULL REFERENCES warehouse.stock_items(id) ON DELETE RESTRICT,
    default_quantity    NUMERIC(18, 3) NOT NULL CHECK (default_quantity > 0),
    -- Required vs optional helps the dispatch UI render the line as
    -- "always include" vs "include if needed".
    required            BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order          INT NOT NULL DEFAULT 0,
    notes               TEXT,
    UNIQUE (template_id, stock_item_id)
);

CREATE INDEX idx_bom_template_item ON warehouse.product_bom_template_items (template_id);

-- Optional FK from wo_dispatches back to the template that seeded it.
-- ON DELETE SET NULL keeps historical dispatches readable even if
-- the template is later removed.
ALTER TABLE warehouse.wo_dispatches
    ADD COLUMN IF NOT EXISTS source_bom_template_id UUID
        REFERENCES warehouse.product_bom_templates(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_dispatch_bom_template
    ON warehouse.wo_dispatches (source_bom_template_id) WHERE source_bom_template_id IS NOT NULL;
