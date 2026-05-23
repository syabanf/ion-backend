package postgres

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// AddOnPurchaseRepository — billing.add_on_purchases driven adapter.
// =====================================================================

type AddOnPurchaseRepository struct {
	pool *pgxpool.Pool
}

func NewAddOnPurchaseRepository(pool *pgxpool.Pool) *AddOnPurchaseRepository {
	return &AddOnPurchaseRepository{pool: pool}
}

var _ port.AddOnPurchaseRepository = (*AddOnPurchaseRepository)(nil)

const addonCols = `
	id, customer_id, addon_sku, COALESCE(addon_name, ''),
	COALESCE(category, 'service'),
	qty, unit_price, total, invoice_id, status,
	valid_from, valid_until, cancelled_at,
	COALESCE(cancel_reason, ''),
	created_at, updated_at
`

func (r *AddOnPurchaseRepository) Create(ctx context.Context, p *domain.AddOnPurchase) error {
	if p == nil {
		return derrors.Validation("addon.nil", "purchase is nil")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.add_on_purchases
			(id, customer_id, addon_sku, addon_name, category,
			 qty, unit_price, total, invoice_id, status,
			 valid_from, valid_until, cancelled_at, cancel_reason,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`,
		p.ID, p.CustomerID, p.AddOnSKU, nullableString(p.AddOnName), string(p.Category),
		p.Quantity, p.UnitPrice, p.Total, p.InvoiceID, string(p.Status),
		p.ValidFrom, p.ValidUntil, p.CancelledAt, nullableString(p.CancelReason),
		p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "addon", "insert add-on purchase")
	}
	return nil
}

func (r *AddOnPurchaseRepository) Update(ctx context.Context, p *domain.AddOnPurchase) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE billing.add_on_purchases
		SET status        = $2,
		    invoice_id    = $3,
		    valid_from    = $4,
		    valid_until   = $5,
		    cancelled_at  = $6,
		    cancel_reason = $7,
		    updated_at    = NOW()
		WHERE id = $1
	`,
		p.ID, string(p.Status), p.InvoiceID,
		p.ValidFrom, p.ValidUntil, p.CancelledAt, nullableString(p.CancelReason),
	)
	if err != nil {
		return mapDBError(err, "addon", "update add-on purchase")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("addon.not_found", "add-on purchase not found")
	}
	return nil
}

func (r *AddOnPurchaseRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.AddOnPurchase, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+addonCols+` FROM billing.add_on_purchases WHERE id = $1`, id)
	p, err := scanAddOn(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *AddOnPurchaseRepository) ListByCustomer(ctx context.Context, customerID uuid.UUID, statuses []string) ([]domain.AddOnPurchase, error) {
	args := []any{customerID}
	q := `SELECT ` + addonCols + ` FROM billing.add_on_purchases WHERE customer_id = $1`
	if len(statuses) > 0 {
		args = append(args, statuses)
		q += ` AND status = ANY($2)`
	}
	q += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "addon.list", "list customer add-ons", err)
	}
	defer rows.Close()
	out := []domain.AddOnPurchase{}
	for rows.Next() {
		p, err := scanAddOn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *AddOnPurchaseRepository) ListExpiring(ctx context.Context, before time.Time, limit int) ([]domain.AddOnPurchase, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+addonCols+` FROM billing.add_on_purchases
		WHERE status = 'active' AND valid_until IS NOT NULL AND valid_until <= $1
		ORDER BY valid_until ASC
		LIMIT $2
	`, before, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "addon.expiring", "list expiring add-ons", err)
	}
	defer rows.Close()
	out := []domain.AddOnPurchase{}
	for rows.Next() {
		p, err := scanAddOn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func scanAddOn(row pgx.Row) (domain.AddOnPurchase, error) {
	var (
		p           domain.AddOnPurchase
		categoryStr string
		statusStr   string
	)
	err := row.Scan(
		&p.ID, &p.CustomerID, &p.AddOnSKU, &p.AddOnName, &categoryStr,
		&p.Quantity, &p.UnitPrice, &p.Total, &p.InvoiceID, &statusStr,
		&p.ValidFrom, &p.ValidUntil, &p.CancelledAt, &p.CancelReason,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.AddOnPurchase{}, derrors.NotFound("addon.not_found", "add-on purchase not found")
	}
	if err != nil {
		return domain.AddOnPurchase{}, derrors.Wrap(derrors.KindInternal, "addon.scan", "scan add-on purchase", err)
	}
	p.Category = domain.AddOnCategory(categoryStr)
	p.Status = domain.AddOnStatus(statusStr)
	return p, nil
}

// =====================================================================
// CatalogReader — crm.product_addons SQL-only read.
//
// We deliberately do not Go-import the crm package. Schema drift in
// product_addons surfaces as a typed scan error here, which is the
// right signal for the audit chain.
// =====================================================================

type CatalogReader struct {
	pool *pgxpool.Pool
}

func NewCatalogReader(pool *pgxpool.Pool) *CatalogReader {
	return &CatalogReader{pool: pool}
}

var _ port.CatalogReader = (*CatalogReader)(nil)

const catalogCols = `
	id, COALESCE(code, ''), COALESCE(name, ''),
	COALESCE(addon_type, 'service'),
	COALESCE(one_time_fee, 0), COALESCE(monthly_fee, 0),
	COALESCE(requires_install, FALSE),
	COALESCE(active, FALSE)
`

func (r *CatalogReader) ListActive(ctx context.Context) ([]port.CatalogItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+catalogCols+` FROM crm.product_addons
		WHERE active = TRUE
		ORDER BY name
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "catalog.list", "list catalog", err)
	}
	defer rows.Close()
	out := []port.CatalogItem{}
	for rows.Next() {
		it, err := scanCatalogItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (r *CatalogReader) FindBySKU(ctx context.Context, sku string) (*port.CatalogItem, error) {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return nil, derrors.Validation("catalog.sku_required", "sku is required")
	}
	// The crm.product_addons table uses `code` as the SKU column.
	// Match on UUID first (allows the mobile app to pass either an id
	// or a code), then fall back to code.
	if u, err := uuid.Parse(sku); err == nil {
		row := r.pool.QueryRow(ctx, `SELECT `+catalogCols+` FROM crm.product_addons WHERE id = $1 AND active = TRUE`, u)
		it, err := scanCatalogItem(row)
		if stderrors.Is(err, pgx.ErrNoRows) {
			// fall through to code lookup
		} else if err == nil {
			return &it, nil
		} else if !derrors.IsNotFound(err) {
			return nil, err
		}
	}
	row := r.pool.QueryRow(ctx, `SELECT `+catalogCols+` FROM crm.product_addons WHERE code = $1 AND active = TRUE`, sku)
	it, err := scanCatalogItem(row)
	if stderrors.Is(err, pgx.ErrNoRows) || derrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func scanCatalogItem(row pgx.Row) (port.CatalogItem, error) {
	var (
		it          port.CatalogItem
		categoryStr string
	)
	err := row.Scan(
		&it.ID, &it.SKU, &it.Name,
		&categoryStr,
		&it.OneTimeFee, &it.MonthlyFee,
		&it.RequiresInstall,
		&it.Active,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return port.CatalogItem{}, derrors.NotFound("catalog.not_found", "catalog item not found")
	}
	if err != nil {
		return port.CatalogItem{}, derrors.Wrap(derrors.KindInternal, "catalog.scan", "scan catalog item", err)
	}
	switch categoryStr {
	case "speed_boost", "bandwidth", "data":
		it.Category = domain.AddOnCategoryDigital
	case "router", "cable", "device", "hardware":
		it.Category = domain.AddOnCategoryPhysical
	default:
		// Falls back to digital for any add-on that doesn't require an
		// install (radius push only); physical otherwise.
		if it.RequiresInstall {
			it.Category = domain.AddOnCategoryPhysical
		} else {
			it.Category = domain.AddOnCategoryDigital
		}
	}
	return it, nil
}

// =====================================================================
// AddOnCRMGateway — sync crm.customer_addons from the billing flow.
//
// The CRM-side portal_auth.go::buyAddon writes this same table; this
// adapter exists so the new /portal/billing/add-ons/purchase path keeps
// the CRM row in sync without ping-ponging through HTTP.
// =====================================================================

type AddOnCRMGateway struct {
	pool *pgxpool.Pool
}

func NewAddOnCRMGateway(pool *pgxpool.Pool) *AddOnCRMGateway {
	return &AddOnCRMGateway{pool: pool}
}

var _ port.AddOnCRMGateway = (*AddOnCRMGateway)(nil)

func (g *AddOnCRMGateway) UpsertCustomerAddon(
	ctx context.Context,
	customerID, addonID uuid.UUID,
	quantity int,
	oneTimeFee, monthlyFee float64,
	status string,
) error {
	// crm.customer_addons doesn't have a uniqueness on (customer_id,
	// addon_id) per the round-1 schema; we insert a new row each time
	// to preserve the purchase history. Idempotency comes from the
	// billing.add_on_purchases UNIQUE constraints (the caller).
	id := uuid.New()
	_, err := g.pool.Exec(ctx, `
		INSERT INTO crm.customer_addons (
			id, customer_id, addon_id, status, quantity,
			one_time_fee, monthly_fee
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
	`,
		id, customerID, addonID, status, quantity,
		oneTimeFee*float64(quantity), monthlyFee*float64(quantity),
	)
	if err != nil {
		return mapDBError(err, "crm_addon", "upsert customer_addons")
	}
	return nil
}

func (g *AddOnCRMGateway) MarkCancelled(ctx context.Context, customerAddonID uuid.UUID, reason string) error {
	_, err := g.pool.Exec(ctx, `
		UPDATE crm.customer_addons
		SET status = 'cancelled', notes = COALESCE(notes, '') || $2, updated_at = NOW()
		WHERE id = $1
	`, customerAddonID, "\ncancelled: "+reason)
	if err != nil {
		return mapDBError(err, "crm_addon", "mark cancelled")
	}
	return nil
}
