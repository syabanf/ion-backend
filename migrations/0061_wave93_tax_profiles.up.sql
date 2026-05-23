-- Wave 93 — Tax compliance bounded context (Phase 1 Enterprise).
--
-- Scope: PKP / Non-PKP company tax profiles + Faktur Pajak DJP scaffold.
-- Covers the 12 "Company Tax Profile" TCs in the Phase 1 Enterprise
-- catalog.
--
-- This is a NEW bounded context — kept deliberately loose-coupled to
-- enterprise.invoices and enterprise.subsidiaries (NO foreign keys
-- across schemas). Cross-context references (subsidiary_id, invoice_id)
-- are stored as plain UUIDs and resolved by the frontend at display
-- time, mirroring the convention used by the enterprise CPQ context.
--
-- The `tax` schema is dedicated so this context can later be extracted
-- to its own service binary without renaming.

CREATE SCHEMA IF NOT EXISTS tax;

-- =====================================================================
-- Company tax profiles
-- =====================================================================
--
-- One profile per subsidiary per effective_from. Profiles are versioned
-- by replacement: when rates change, insert a new row with the new
-- effective_from. The "active" profile at any timestamp is the row
-- with the highest effective_from <= timestamp and (effective_to is
-- NULL or effective_to >= timestamp).
--
-- PPN default is the post-2022 Indonesian standard rate (11%). PPh 23
-- is 2% for jasa lain (default). PPh Final is 0 unless the subsidiary
-- opts in (e.g., UMKM scheme).
CREATE TABLE tax.company_tax_profiles (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    subsidiary_id   UUID NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    npwp            TEXT NOT NULL DEFAULT '',
    is_pkp          BOOLEAN NOT NULL DEFAULT FALSE,
    ppn_rate        NUMERIC(5, 4) NOT NULL DEFAULT 0.1100,
    pph23_rate      NUMERIC(5, 4) NOT NULL DEFAULT 0.0200,
    pph_final_rate  NUMERIC(5, 4) NOT NULL DEFAULT 0.0000,
    effective_from  DATE NOT NULL,
    effective_to    DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (subsidiary_id, effective_from)
);

CREATE INDEX idx_company_tax_profiles_subsidiary_effective
    ON tax.company_tax_profiles (subsidiary_id, effective_from DESC);

-- =====================================================================
-- Faktur Pajak records
-- =====================================================================
--
-- jenis_faktur codes (DJP):
--   01 — Standard / Penjualan ke pihak lain.
--   02 — Penyerahan kepada pemungut Bendaharawan.
--   03 — Penyerahan kepada pemungut PPN selain Bendaharawan.
--   04 — Penyerahan dengan DPP Nilai Lain.
--   06 — Penyerahan lainnya (termasuk turis asing).
--   07 — Penyerahan dengan fasilitas (tidak dipungut / ditanggung
--        pemerintah).
--   08 — Penyerahan yang dibebaskan dari pengenaan PPN.
--
-- nomor_seri is the DJP-issued serial number. NULL until SubmitFaktur
-- successfully calls the DJP e-Faktur API and persists the response.
-- UNIQUE allows multiple drafts with NULL nomor_seri but rejects
-- duplicate issued serials.
CREATE TABLE tax.faktur_pajak_records (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    invoice_id              UUID NOT NULL,
    subsidiary_id           UUID,
    nomor_seri              TEXT UNIQUE,
    jenis_faktur            TEXT NOT NULL DEFAULT '01'
        CHECK (jenis_faktur IN ('01','02','03','04','06','07','08')),
    tanggal_faktur          DATE,
    npwp_lawan_transaksi    TEXT NOT NULL DEFAULT '',
    dpp                     NUMERIC(18, 2) NOT NULL DEFAULT 0,
    ppn                     NUMERIC(18, 2) NOT NULL DEFAULT 0,
    status                  TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','submitted','approved','rejected','cancelled')),
    djp_response_payload    JSONB,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_faktur_pajak_invoice
    ON tax.faktur_pajak_records (invoice_id);

CREATE INDEX idx_faktur_pajak_status_created
    ON tax.faktur_pajak_records (status, created_at);

-- =====================================================================
-- Seed — one demo profile so the read endpoints have something to show
-- in dev / smoke tests. Uses a stable UUID for the subsidiary so a
-- second run of the migration suite (or downstream test fixtures) can
-- reference it deterministically.
-- =====================================================================
INSERT INTO tax.company_tax_profiles
    (subsidiary_id, name, npwp, is_pkp,
     ppn_rate, pph23_rate, pph_final_rate, effective_from)
VALUES
    ('00000000-0000-0000-0000-000000000001',
     'ION Demo PT (PKP)',
     '01.234.567.8-901.000',
     TRUE,
     0.1100, 0.0200, 0.0000,
     DATE '2024-01-01');
