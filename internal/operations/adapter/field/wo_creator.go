// Wave 125 — SQL-only bridge from operations into the field bounded
// context. Handles Bulk WO Creation apply + duplicate-open-WO probe +
// template/customer code lookup.
//
// No Go imports of internal/field — the bridge owns the cross-context
// access via raw SQL against field.* tables.
package field

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WOCreatorBridge owns the SQL-only access into field.* tables.
type WOCreatorBridge struct {
	pool *pgxpool.Pool
}

func NewWOCreatorBridge(pool *pgxpool.Pool) *WOCreatorBridge {
	return &WOCreatorBridge{pool: pool}
}

var (
	_ port.WOCreator                   = (*WOCreatorBridge)(nil)
	_ domain.WOCreationValidatorPort   = (*WOCreatorBridge)(nil)
)

func (b *WOCreatorBridge) schemaInstalled(ctx context.Context) (bool, error) {
	var n int
	err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = 'field' AND table_name = 'work_orders'
	`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Create inserts a row into field.work_orders. Returns the new WO id on
// success.
//
// Concurrent WO block — TC-BWO-003: if the customer already has a
// non-terminal WO of the same wo_type, the bridge returns a KindConflict
// with code 'bulk.wo_duplicate'. The executor catches this and marks
// the item Duplicate, not Failed.
func (b *WOCreatorBridge) Create(ctx context.Context, item *domain.BulkWOCreationItem) (uuid.UUID, error) {
	ok, err := b.schemaInstalled(ctx)
	if err != nil {
		return uuid.Nil, derrors.Wrap(derrors.KindInternal, "bulk.field_schema_probe",
			"probe field schema", err)
	}
	if !ok {
		return uuid.Nil, derrors.New(derrors.KindUnavailable, "bulk.target_schema_missing",
			"field.work_orders not installed in this database")
	}

	woType := item.WOType
	if woType == "" {
		woType = "maintenance"
	}
	if !validWOType(woType) {
		return uuid.Nil, derrors.Validation("bulk.wo_type_invalid",
			fmt.Sprintf("wo_type %q not in {new_installation, maintenance, termination}", woType))
	}

	// Duplicate-open-WO check.
	open, err := b.CustomerHasOpenWOOfType(ctx, item.CustomerID, woType)
	if err != nil {
		return uuid.Nil, err
	}
	if open {
		return uuid.Nil, derrors.Conflict("bulk.wo_duplicate",
			"customer already has an open WO of this type")
	}

	// Customer must exist + have an address (the WO table requires
	// address NOT NULL).
	var address string
	err = b.pool.QueryRow(ctx, `
		SELECT address FROM crm.customers WHERE id = $1
	`, item.CustomerID).Scan(&address)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, derrors.NotFound("bulk.customer_missing",
			"customer not found in crm.customers")
	}
	if err != nil {
		return uuid.Nil, mapDBError(err, "bulk.customer_lookup", "probe crm.customers")
	}

	newID := uuid.New()
	woNumber := "BWO-" + time.Now().UTC().Format("20060102-150405") + "-" + newID.String()[:8]
	_, err = b.pool.Exec(ctx, `
		INSERT INTO field.work_orders
			(id, wo_number, customer_id, wo_type, address,
			 status, scheduled_date, notes)
		VALUES ($1, $2, $3, $4, $5, 'created', $6, $7)
	`, newID, woNumber, item.CustomerID, woType, address,
		item.ScheduledAt, "bulk:"+item.BulkJobID.String())
	if err != nil {
		return uuid.Nil, mapDBError(err, "bulk.wo_insert", "insert field.work_order")
	}
	return newID, nil
}

// WOTemplateExists — domain.WOCreationValidatorPort. The Wave 71 field
// module doesn't have a wo_templates table yet — Wave 125 treats a nil
// templateID as "no template required" and returns true. Once the
// templates table lands, the bridge can probe it.
func (b *WOCreatorBridge) WOTemplateExists(ctx context.Context, templateID *uuid.UUID) (bool, error) {
	if templateID == nil {
		return true, nil
	}
	// Probe for the table; if not present, accept the templateID as opaque.
	var n int
	if err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = 'field' AND table_name = 'wo_templates'
	`).Scan(&n); err != nil {
		return false, mapDBError(err, "bulk.template_probe", "probe field schema")
	}
	if n == 0 {
		return true, nil
	}
	if err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.wo_templates WHERE id = $1
	`, *templateID).Scan(&n); err != nil {
		return false, mapDBError(err, "bulk.template_lookup", "probe field.wo_templates")
	}
	return n > 0, nil
}

// CustomerHasOpenWOOfType — domain.WOCreationValidatorPort.
func (b *WOCreatorBridge) CustomerHasOpenWOOfType(ctx context.Context, customerID uuid.UUID, woType string) (bool, error) {
	var n int
	err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM field.work_orders
		 WHERE customer_id = $1
		   AND wo_type     = $2
		   AND status NOT IN ('completed','cancelled')
	`, customerID, woType).Scan(&n)
	if err != nil {
		return false, mapDBError(err, "bulk.wo_open_probe", "probe field.work_orders")
	}
	return n > 0, nil
}

// =====================================================================
// CSV lookup port — wo_template_code resolver
// =====================================================================

// WOTemplateIDByCode resolves a template code into a UUID, or returns
// (nil, nil) if the template table isn't installed yet.
func (b *WOCreatorBridge) WOTemplateIDByCode(ctx context.Context, code string) (*uuid.UUID, error) {
	var n int
	if err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = 'field' AND table_name = 'wo_templates'
	`).Scan(&n); err != nil {
		return nil, mapDBError(err, "bulk.template_probe", "probe field schema")
	}
	if n == 0 {
		// No templates table yet — treat the code as an opaque tag and
		// let the executor proceed without a template id.
		return nil, nil
	}
	var id uuid.UUID
	err := b.pool.QueryRow(ctx, `
		SELECT id FROM field.wo_templates WHERE code = $1
	`, code).Scan(&id)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bulk.template_lookup", "probe field.wo_templates")
	}
	return &id, nil
}

// validWOType mirrors the CHECK on field.work_orders.wo_type.
func validWOType(t string) bool {
	switch t {
	case "new_installation", "maintenance", "termination":
		return true
	}
	return false
}
