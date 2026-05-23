// Wave 105 — bulk-insert helper for perf testing.
//
// Lives in a separate file from boq_line_repo.go so the production
// repo file is untouched. The Create path in boq_line_repo.go does
// a single-row INSERT and is the only path used by usecase code;
// bulk insert exists exclusively to seed the 100k-line BOQ perf
// fixture in test/perf/enterprise_bulk_test.go (TC-NFR-004).
//
// Do NOT call this from business logic — it skips the
// status/snapshot consistency rules the domain constructor enforces.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ion-core/backend/internal/enterprise/domain"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkInsertLines copy-inserts a batch of BOQ lines for a single BOQ
// version. Uses pgx's CopyFrom — orders of magnitude faster than 100k
// individual INSERTs, which is the difference between a 30s seed and
// a 10-minute one.
//
// Caller is responsible for setting all required snapshot fields +
// status (typically domain.BOQLineStatusHasCost or
// BOQLineStatusAwaitingProviderInput). Sort order auto-increments
// based on slice index — caller doesn't need to pre-set it.
func (r *BOQLineRepository) BulkInsertLines(ctx context.Context, lines []domain.BOQLine) (int64, error) {
	if len(lines) == 0 {
		return 0, nil
	}

	rows := make([][]any, len(lines))
	for i, l := range lines {
		// Auto-assign sort_order from the slice index when the caller
		// left it at zero. Saves the perf test from having to pre-
		// number every line.
		sortOrder := l.SortOrder
		if sortOrder == 0 {
			sortOrder = i + 1
		}
		rows[i] = []any{
			l.ID, l.BOQVersionID, l.PricebookLineID,
			l.SKU, l.Name, l.Unit,
			l.BasePriceSnapshot, l.MinMarginSnapshot, l.MaxDiscountSnapshot,
			l.AssignedProviderCompanyID, l.ProviderUserID,
			l.VendorUnitCost, l.SellUnitPrice, l.Quantity, l.LineDiscountPct,
			l.SLATemplateID, string(l.Status), l.Notes, sortOrder,
			l.VendorDueAt,
			l.CreatedAt, l.UpdatedAt,
		}
	}

	n, err := r.pool.CopyFrom(
		ctx,
		pgx.Identifier{"enterprise", "boq_lines"},
		[]string{
			"id", "boq_version_id", "pricebook_line_id",
			"sku", "name", "unit",
			"base_price_snapshot", "min_margin_snapshot", "max_discount_snapshot",
			"assigned_provider_company_id", "provider_user_id",
			"vendor_unit_cost", "sell_unit_price", "quantity", "line_discount_pct",
			"sla_template_id", "status", "notes", "sort_order",
			"vendor_due_at",
			"created_at", "updated_at",
		},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.boq_line_bulk_insert", "bulk insert boq lines", err)
	}
	return n, nil
}
