package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ChecklistRepository struct {
	pool *pgxpool.Pool
}

func NewChecklistRepository(pool *pgxpool.Pool) *ChecklistRepository {
	return &ChecklistRepository{pool: pool}
}

var _ port.ChecklistRepository = (*ChecklistRepository)(nil)

// FindTemplateFor finds the most specific template matching the WO. We
// try (wo_type, product_type, maintenance_subtype) first, then fall back
// to (wo_type, product_type, null). Returns NotFound if neither exists.
func (r *ChecklistRepository) FindTemplateFor(ctx context.Context, woType domain.WOType, productType, maintSubtype string) (*domain.ChecklistTemplate, []domain.ChecklistTemplateItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, wo_type, product_type, COALESCE(maintenance_subtype,''),
		       min_photos_required, gps_stamp_on_photos, active, created_at, updated_at
		FROM field.wo_checklist_templates
		WHERE wo_type = $1
		  AND product_type = $2
		  AND (maintenance_subtype = $3 OR (maintenance_subtype IS NULL AND $3 = ''))
		  AND active
		ORDER BY (maintenance_subtype IS NULL) ASC
		LIMIT 1
	`, string(woType), productType, maintSubtype)

	var (
		t      domain.ChecklistTemplate
		wType  string
	)
	err := row.Scan(&t.ID, &wType, &t.ProductType, &t.MaintenanceSubtype,
		&t.MinPhotosRequired, &t.GPSStampOnPhotos, &t.Active, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, derrors.NotFound("checklist.template_not_found",
			"no checklist template for this WO type/product")
	}
	if err != nil {
		return nil, nil, derrors.Wrap(derrors.KindInternal, "db.tpl_scan", "scan template", err)
	}
	t.WOType = domain.WOType(wType)

	rows, err := r.pool.Query(ctx, `
		SELECT id, template_id, item_order, item_type, label, required,
		       COALESCE(photo_tag,''), gps_required, min_accuracy_meters
		FROM field.wo_checklist_template_items
		WHERE template_id = $1
		ORDER BY item_order
	`, t.ID)
	if err != nil {
		return nil, nil, derrors.Wrap(derrors.KindInternal, "db.tpl_items_query", "query items", err)
	}
	defer rows.Close()
	items := []domain.ChecklistTemplateItem{}
	for rows.Next() {
		var (
			it    domain.ChecklistTemplateItem
			itype string
		)
		if err := rows.Scan(&it.ID, &it.TemplateID, &it.ItemOrder, &itype,
			&it.Label, &it.Required, &it.PhotoTag, &it.GPSRequired, &it.MinAccuracyMeters); err != nil {
			return nil, nil, derrors.Wrap(derrors.KindInternal, "db.tpl_item_scan", "scan item", err)
		}
		it.ItemType = domain.ItemType(itype)
		items = append(items, it)
	}
	return &t, items, nil
}

// UpsertResponse — one row per (wo_id, template_item_id). The DB UNIQUE
// FindItem fetches a single template item — used by the M5 r3 GPS gate.
func (r *ChecklistRepository) FindItem(ctx context.Context, id uuid.UUID) (*domain.ChecklistTemplateItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, template_id, item_order, item_type, label, required,
		       COALESCE(photo_tag,''), gps_required, min_accuracy_meters
		FROM field.wo_checklist_template_items
		WHERE id = $1
	`, id)
	var (
		it    domain.ChecklistTemplateItem
		itype string
	)
	err := row.Scan(&it.ID, &it.TemplateID, &it.ItemOrder, &itype,
		&it.Label, &it.Required, &it.PhotoTag, &it.GPSRequired, &it.MinAccuracyMeters)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("checklist.item_not_found", "template item not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.tpl_item_scan", "scan item", err)
	}
	it.ItemType = domain.ItemType(itype)
	return &it, nil
}

// makes this an INSERT … ON CONFLICT DO UPDATE so resubmits overwrite.
func (r *ChecklistRepository) UpsertResponse(ctx context.Context, rsp *domain.ChecklistResponse) (*domain.ChecklistResponse, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO field.wo_checklist_responses (
			id, wo_id, template_item_id, response_text, file_url,
			gps_lat, gps_lng, gps_accuracy_m, submitted_by, submitted_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (wo_id, template_item_id) DO UPDATE SET
		    response_text = EXCLUDED.response_text,
		    file_url      = EXCLUDED.file_url,
		    gps_lat       = EXCLUDED.gps_lat,
		    gps_lng       = EXCLUDED.gps_lng,
		    gps_accuracy_m= EXCLUDED.gps_accuracy_m,
		    submitted_by  = EXCLUDED.submitted_by,
		    submitted_at  = EXCLUDED.submitted_at
		RETURNING id, wo_id, template_item_id, COALESCE(response_text,''),
		          COALESCE(file_url,''), gps_lat, gps_lng, gps_accuracy_m,
		          submitted_by, submitted_at
	`,
		rsp.ID, rsp.WOID, rsp.TemplateItemID,
		nullableString(rsp.ResponseText), nullableString(rsp.FileURL),
		rsp.GPSLat, rsp.GPSLng, rsp.GPSAccuracyM,
		rsp.SubmittedBy, rsp.SubmittedAt,
	)
	var out domain.ChecklistResponse
	if err := row.Scan(&out.ID, &out.WOID, &out.TemplateItemID,
		&out.ResponseText, &out.FileURL,
		&out.GPSLat, &out.GPSLng, &out.GPSAccuracyM,
		&out.SubmittedBy, &out.SubmittedAt); err != nil {
		return nil, mapDBError(err, "checklist.response", "save response")
	}
	return &out, nil
}

func (r *ChecklistRepository) ListResponses(ctx context.Context, woID uuid.UUID) ([]domain.ChecklistResponse, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, wo_id, template_item_id, COALESCE(response_text,''),
		       COALESCE(file_url,''), gps_lat, gps_lng, gps_accuracy_m,
		       submitted_by, submitted_at
		FROM field.wo_checklist_responses
		WHERE wo_id = $1
	`, woID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.resp_list", "list responses", err)
	}
	defer rows.Close()
	out := []domain.ChecklistResponse{}
	for rows.Next() {
		var r domain.ChecklistResponse
		if err := rows.Scan(&r.ID, &r.WOID, &r.TemplateItemID, &r.ResponseText,
			&r.FileURL, &r.GPSLat, &r.GPSLng, &r.GPSAccuracyM,
			&r.SubmittedBy, &r.SubmittedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.resp_scan", "scan response", err)
		}
		out = append(out, r)
	}
	return out, nil
}
