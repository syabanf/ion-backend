package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// recordInternalTransactionsOnApproval is the BOQ-approval hook for the
// sub-company revenue ledger (PRD §7.3). One ledger row per BOQ line
// that has both a vendor_unit_cost AND an assigned provider company.
//
// Idempotent via the unique index on internal_transactions.boq_line_id
// (ON CONFLICT DO NOTHING in the repo).
//
// Best-effort: caller logs but does NOT roll back the approval if this
// fails. The ledger can be backfilled later from the BOQ snapshot.
func (s *Service) recordInternalTransactionsOnApproval(ctx context.Context, b *domain.BOQ) error {
	if s.internalTxs == nil || s.boqLines == nil {
		return nil
	}
	lines, err := s.boqLines.ListByBOQ(ctx, b.ID)
	if err != nil {
		return err
	}
	// Build one transaction per qualifying line.
	now := time.Now().UTC()
	currency := "IDR"
	out := make([]domain.InternalTransaction, 0, len(lines))
	for _, l := range lines {
		// Skip if cost or provider is missing — non-vendor-supplied
		// items shouldn't book sub-company revenue.
		if l.VendorUnitCost == nil || l.AssignedProviderCompanyID == nil {
			continue
		}
		var quoteID *uuid.UUID
		// Quote linkage: best-effort look-up via Phase-4a repo.
		if s.quotations != nil {
			if q, err := s.quotations.FindLatestForBOQ(ctx, b.ID); err == nil && q != nil {
				id := q.ID
				quoteID = &id
			}
		}
		out = append(out, domain.InternalTransaction{
			ID:              uuid.New(),
			BOQVersionID:    b.ID,
			BOQLineID:       l.ID,
			QuotationID:     quoteID,
			VendorCompanyID: l.AssignedProviderCompanyID,
			SellAmount:      l.LineSellTotal(),
			CostAmount:      l.LineCostTotal(),
			// MarginAmount is a generated DB column; we send 0 here, the
			// stored value comes back from any subsequent SELECT.
			Currency:     currency,
			RecognizedAt: now,
			Notes:        "BOQ approval auto-recognition",
			CreatedAt:    now,
		})
	}
	return s.internalTxs.CreateBatch(ctx, out)
}

// ListInternalTransactionsByBOQ returns the sub-company revenue ledger
// scoped to a BOQ version. Empty when the BOQ isn't yet approved (the
// ledger is populated by the approval hook) or when no qualifying lines
// existed at approval time.
func (s *Service) ListInternalTransactionsByBOQ(
	ctx context.Context,
	boqVersionID uuid.UUID,
) ([]domain.InternalTransaction, error) {
	if s.internalTxs == nil {
		return nil, nil
	}
	return s.internalTxs.ListByBOQ(ctx, boqVersionID)
}
