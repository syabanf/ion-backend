package domain

import (
	"time"

	"github.com/google/uuid"
)

// InternalTransaction is one row in the sub-company revenue ledger
// (PRD §7.3 / Appendix B). Generated on BOQ approval — one per BOQ
// line that has both a vendor_unit_cost AND an assigned provider
// company. Aggregating these by vendor_company_id yields the gross
// margin recognized per internal vendor.
//
// Two reasons we don't accumulate live (i.e. read from boq_lines on
// demand):
//   1. Once approved, the BOQ is immutable; the ledger should reflect
//      the snapshot at the moment of approval, not whatever the lines
//      look like later.
//   2. Reporting queries get to skip the join through BOQ + lines.
type InternalTransaction struct {
	ID              uuid.UUID
	BOQVersionID    uuid.UUID
	BOQLineID       uuid.UUID
	QuotationID     *uuid.UUID
	VendorCompanyID *uuid.UUID
	SellAmount      float64
	CostAmount      float64
	MarginAmount    float64 // generated column in DB; copied here for reads
	Currency        string
	RecognizedAt    time.Time
	Notes           string
	CreatedAt       time.Time
}
