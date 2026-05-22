// Package field adapts the field WO table to billing.port.FieldGateway.
//
// Round-3 in-process / direct-SQL approach: the billing service writes
// straight into field.work_orders to mint termination WOs. We don't
// route through the field usecase because that would require billing-svc
// to wire the full field service (all 7 repos + CRM gateway), and we
// only need the table-insert behaviour anyway.
//
// Round-4 swaps this for an HTTP call to field-svc.
package field

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/internal/field/domain"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Gateway struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(pool *pgxpool.Pool) *Gateway {
	return &Gateway{pool: pool, log: slog.Default()}
}

var _ port.FieldGateway = (*Gateway)(nil)

func (g *Gateway) CreateTerminationWO(ctx context.Context, in port.CreateTerminationWOInput) (uuid.UUID, error) {
	w, err := domain.NewTerminationWO(in.OrderID, in.CustomerID, in.Address)
	if err != nil {
		return uuid.Nil, err
	}
	w.BranchID = in.BranchID
	if in.CreatedBy != uuid.Nil {
		cb := in.CreatedBy
		w.CreatedBy = &cb
	}
	w.Notes = in.Notes
	w.Status = domain.WOStatusUnassigned
	w.UpdatedAt = time.Now().UTC()

	_, err = g.pool.Exec(ctx, `
		INSERT INTO field.work_orders (
		    id, wo_number, order_id, customer_id, wo_type,
		    product_type, maintenance_subtype, address, branch_id,
		    priority, status, is_emergency, is_cross_area, notes,
		    created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
	`,
		w.ID, w.WONumber, w.OrderID, w.CustomerID, string(w.WOType),
		w.ProductType, nullableString(w.MaintenanceSubtype), w.Address, w.BranchID,
		string(w.Priority), string(w.Status), w.IsEmergency, w.IsCrossArea,
		nullableString(w.Notes), w.CreatedBy, w.CreatedAt,
	)
	if err != nil {
		return uuid.Nil, derrors.Wrap(derrors.KindInternal, "field.wo_insert", "insert termination WO", err)
	}

	// Gap C — audit trail for the termination → WO hand-off. There's
	// no centralised audit writer yet (planned alongside the broadband
	// happy-path work); slog gives ops a structured record that flows
	// into the standard log pipeline until that lands.
	g.log.InfoContext(ctx, "termination_wo_created",
		slog.String("wo_id", w.ID.String()),
		slog.String("wo_number", w.WONumber),
		slog.String("customer_id", w.CustomerID.String()),
		slog.Any("order_id", w.OrderID),
		slog.Any("branch_id", w.BranchID),
	)

	return w.ID, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
