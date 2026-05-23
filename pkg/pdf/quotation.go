// Package pdf produces PDF artifacts for ION Core's commercial flows.
//
// Currently exposes one entrypoint — RenderQuotation — used by the
// enterprise context to materialize the customer-facing quotation
// document from a BOQ + lines snapshot.
//
// Why gofpdf:
//   - Pure Go, zero external binaries → keeps the deploy story simple
//     and avoids Chromium-headless ops baggage at MVP scale.
//   - Deterministic output when given the same input — important
//     because we hash the output bytes for integrity verification
//     (CPQ TC-QT-002). gofpdf's default CreationDate metadata is the
//     only non-deterministic field, and we override it via
//     SetCreationDate so two renders of the same QuotationData
//     produce identical bytes.
//
// When we outgrow this (custom letterheads, complex tables, signature
// blocks), swap the implementation for a headless-Chromium service
// behind the same RenderQuotation signature.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"
)

// QuotationLine is the per-line payload baked into the PDF body table.
// Trimmed to the fields the customer-facing document actually shows —
// internal-only fields (vendor cost, margin, snapshot hashes) stay
// behind in the BOQ record.
type QuotationLine struct {
	SKU           string
	Name          string
	Unit          string
	Quantity      float64
	SellUnitPrice float64
	DiscountPct   float64
	SLALabel      string
}

// QuotationData is the input payload for RenderQuotation. It carries
// every value the rendered PDF needs to display + the canonical
// creation timestamp used for determinism.
type QuotationData struct {
	// Header
	QuotationNumber string
	VersionNo       int
	IssuedAt        time.Time // pinned for determinism (NFR-007 hash stability)
	ValidUntil      time.Time
	Currency        string
	// Vendor / supplier branding — the issuing company. Hard-coded for
	// MVP; future Admin module surfaces this as configurable.
	IssuerName    string
	IssuerAddress string
	IssuerEmail   string
	// Customer / opportunity context
	OpportunityNumber string
	AccountName       string
	PICName           string
	PICTitle          string
	PICEmail          string
	// Body
	Lines     []QuotationLine
	SellTotal float64
	// Wave 106 — tax breakdown (TC-QT-010/011). When SubtotalAmount > 0
	// the footer renders a "Pajak (PPN)" row between line subtotal and
	// grand total: subtotal + (TaxPct% * subtotal) = SellTotal. Empty /
	// zero values fall back to the legacy "grand total only" footer so
	// pre-tax-snapshot quotations keep rendering unchanged.
	SubtotalAmount float64
	TaxPct         float64
	TaxAmount      float64
	// Free-text terms block (optional)
	Notes string
}

// RenderQuotation marshals the data to a PDF byte slice. Two calls
// with the same `QuotationData` produce identical bytes (verified by
// TC-NFR-007-style assertions in the smoke test).
func RenderQuotation(d QuotationData) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	// Determinism: gofpdf stamps CreationDate from time.Now() by
	// default. Override with the data's IssuedAt so the same payload
	// always produces the same bytes. (Single-arg variant in this
	// version of gofpdf — UTC ensures no timezone drift.)
	pdf.SetCreationDate(d.IssuedAt.UTC())
	pdf.SetModificationDate(d.IssuedAt.UTC())
	pdf.AddPage()

	// =================== Header band ===================
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(0, 8, "QUOTATION", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(0, 5, d.IssuerName, "", 1, "L", false, 0, "")
	if d.IssuerAddress != "" {
		pdf.CellFormat(0, 4, d.IssuerAddress, "", 1, "L", false, 0, "")
	}
	if d.IssuerEmail != "" {
		pdf.CellFormat(0, 4, d.IssuerEmail, "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	// Quote-number + dates row (two columns)
	pdf.SetFont("Helvetica", "", 9)
	row := func(label, value string) {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(40, 5, label, "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		pdf.CellFormat(0, 5, value, "", 1, "L", false, 0, "")
	}
	row("Quotation #:", fmt.Sprintf("%s · v%d", d.QuotationNumber, d.VersionNo))
	row("Opportunity:", d.OpportunityNumber)
	row("Issued:", d.IssuedAt.UTC().Format("2006-01-02"))
	row("Valid until:", d.ValidUntil.UTC().Format("2006-01-02"))
	row("Currency:", d.Currency)
	pdf.Ln(4)

	// =================== Customer block ===================
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(0, 6, "Customer", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(0, 5, d.AccountName, "", 1, "L", false, 0, "")
	if d.PICName != "" {
		pic := d.PICName
		if d.PICTitle != "" {
			pic += " (" + d.PICTitle + ")"
		}
		pdf.CellFormat(0, 5, pic, "", 1, "L", false, 0, "")
	}
	if d.PICEmail != "" {
		pdf.CellFormat(0, 5, d.PICEmail, "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	// =================== Line items table ===================
	// Columns: # / SKU / Description / Qty / Unit price / Disc% / Subtotal / SLA
	// Widths sum to ~190 (A4 landscape width minus margins) — pad
	// description so the row stays one line even on long product names.
	pdf.SetFont("Helvetica", "B", 9)
	headers := []struct {
		w    float64
		text string
		algn string
	}{
		{8, "#", "C"},
		{22, "SKU", "L"},
		{60, "Description", "L"},
		{12, "Qty", "R"},
		{22, "Unit price", "R"},
		{12, "Disc%", "R"},
		{28, "Subtotal", "R"},
		{26, "SLA", "L"},
	}
	pdf.SetFillColor(240, 240, 240)
	for _, h := range headers {
		pdf.CellFormat(h.w, 6, h.text, "1", 0, h.algn, true, 0, "")
	}
	pdf.Ln(-1)

	pdf.SetFont("Helvetica", "", 8)
	for i, l := range d.Lines {
		gross := l.SellUnitPrice * l.Quantity
		subtotal := gross
		if l.DiscountPct > 0 {
			subtotal = gross * (1 - l.DiscountPct/100.0)
		}
		discDisplay := "-"
		if l.DiscountPct > 0 {
			discDisplay = fmt.Sprintf("%.2f", l.DiscountPct)
		}
		cells := []struct {
			w    float64
			text string
			algn string
		}{
			{8, fmt.Sprintf("%d", i+1), "C"},
			{22, l.SKU, "L"},
			{60, l.Name, "L"},
			{12, formatQty(l.Quantity, l.Unit), "R"},
			{22, formatMoney(l.SellUnitPrice), "R"},
			{12, discDisplay, "R"},
			{28, formatMoney(subtotal), "R"},
			{26, l.SLALabel, "L"},
		}
		for _, c := range cells {
			pdf.CellFormat(c.w, 5, c.text, "1", 0, c.algn, false, 0, "")
		}
		pdf.Ln(-1)
	}

	// =================== Total band ===================
	//
	// Wave 106 — when a tax breakdown is present (TC-QT-010/011) we
	// render Subtotal, Pajak (PPN), and Grand Total as three stacked
	// rows so the FE matches the e-Faktur format. When the BOQ wasn't
	// stamped with a tax snapshot (legacy / pre-Wave-101) we fall back
	// to the single-line grand-total footer the older renderer used,
	// so existing tests + downstream hash-stable expectations stay
	// stable.
	pdf.Ln(2)
	if d.SubtotalAmount > 0 && d.TaxAmount >= 0 {
		// Three-row footer: Subtotal | Pajak (PPN x%) | GRAND TOTAL.
		pdf.SetFont("Helvetica", "", 10)
		pdf.CellFormat(136, 6, "Subtotal", "", 0, "R", false, 0, "")
		pdf.CellFormat(28, 6, formatMoney(d.SubtotalAmount), "T", 0, "R", false, 0, "")
		pdf.CellFormat(26, 6, "", "", 1, "L", false, 0, "")

		taxLabel := "Pajak (PPN)"
		if d.TaxPct > 0 {
			taxLabel = fmt.Sprintf("Pajak (PPN %.0f%%)", d.TaxPct)
		}
		pdf.CellFormat(136, 6, taxLabel, "", 0, "R", false, 0, "")
		pdf.CellFormat(28, 6, formatMoney(d.TaxAmount), "", 0, "R", false, 0, "")
		pdf.CellFormat(26, 6, "", "", 1, "L", false, 0, "")

		pdf.SetFont("Helvetica", "B", 11)
		pdf.CellFormat(136, 7, "GRAND TOTAL", "", 0, "R", false, 0, "")
		pdf.CellFormat(28, 7, formatMoney(d.SellTotal), "T", 0, "R", false, 0, "")
		pdf.CellFormat(26, 7, "", "", 1, "L", false, 0, "")
	} else {
		pdf.SetFont("Helvetica", "B", 11)
		// Empty spacer so the total stays right-aligned over the subtotal column.
		pdf.CellFormat(136, 7, "GRAND TOTAL", "", 0, "R", false, 0, "")
		pdf.CellFormat(28, 7, formatMoney(d.SellTotal), "T", 0, "R", false, 0, "")
		pdf.CellFormat(26, 7, "", "", 1, "L", false, 0, "")
	}
	pdf.Ln(2)

	// =================== Notes / terms ===================
	if d.Notes != "" {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(0, 5, "Notes", "", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		// MultiCell wraps long text; we keep it inside the page width.
		pdf.MultiCell(0, 4.5, d.Notes, "", "L", false)
		pdf.Ln(2)
	}

	pdf.SetFont("Helvetica", "I", 7)
	pdf.CellFormat(0, 4, "This quotation is electronically generated by ION Core. No signature required.", "", 1, "L", false, 0, "")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render quotation pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// =====================================================================
// Helpers
// =====================================================================

func formatMoney(n float64) string {
	// Round to whole numbers — quotations to enterprise customers use
	// integer prices in IDR. Decimal currencies (USD etc.) would want
	// 2 places; defer that until we support multi-currency formatting.
	s := fmt.Sprintf("%.0f", n)
	// Manual thousands separator (Indonesian convention: dot every 3).
	// strconv.FormatFloat doesn't expose locale-aware grouping, so we
	// roll it ourselves to avoid pulling in golang.org/x/text just
	// for this one helper.
	return commaize(s)
}

func formatQty(n float64, unit string) string {
	// Strip trailing zeros from the qty so "10.000 month" doesn't
	// look weirder than "10 month".
	s := fmt.Sprintf("%g", n)
	if unit != "" && unit != "unit" {
		s += " " + unit
	}
	return s
}

func commaize(s string) string {
	// Split sign + digits + (no decimals in our money output).
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	for i, ch := range s {
		// Insert "." every 3 from the right.
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(ch)
	}
	out := b.String()
	if neg {
		return "-" + out
	}
	return out
}
