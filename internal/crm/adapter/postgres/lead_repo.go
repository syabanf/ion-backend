package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/cryptutil"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type LeadRepository struct {
	pool   *pgxpool.Pool
	sealer *cryptutil.Sealer // optional; when set, NIK is stored encrypted
}

func NewLeadRepository(pool *pgxpool.Pool) *LeadRepository {
	return &LeadRepository{pool: pool}
}

// WithSealer enables at-rest encryption of NIK on this repo. See
// CustomerRepository.WithSealer for the rationale and rollout shape.
func (r *LeadRepository) WithSealer(s *cryptutil.Sealer) *LeadRepository {
	r.sealer = s
	return r
}

// sealNIK encrypts a NIK for storage post-migration-0018 (which dropped
// the plaintext column). Empty plaintext writes NULL. Without a sealer
// the repo refuses — KTP_ENC_KEY must be wired in any binary that
// persists lead rows.
func (r *LeadRepository) sealNIK(nik string) ([]byte, error) {
	if nik == "" {
		return nil, nil
	}
	if r.sealer == nil {
		return nil, derrors.New(derrors.KindInternal,
			"lead.ktp_sealer_missing",
			"KTP_ENC_KEY is required to persist NIK")
	}
	return r.sealer.Seal(nik)
}

var _ port.LeadRepository = (*LeadRepository)(nil)

// leadSelect returns the lead row joined with friendly names for product,
// branch, and sales person. We pull all three as left joins so the lead row
// always returns; null-name columns are coalesced empty to keep scans simple.
// Wave 76 (TC-CRM-010): join crm.customers on referrer_customer_id so
// the wire DTO can render the referrer's full name instead of the raw
// UUID. The join is LEFT so leads without a referrer still scan; the
// `referrer_name` column always returns (empty string when absent).
const leadSelect = `
SELECT l.id, l.lead_number, l.status,
       l.full_name, l.phone, COALESCE(l.email,''), l.nik_encrypted,
       l.address, l.gps_lat, l.gps_lng,
       l.coverage_verdict, l.coverage_snapshot,
       l.accept_excess_cable, l.nearest_node_id, l.cable_distance_m, l.excess_charge,
       l.branch_id, l.product_id, l.sales_id, l.source, COALESCE(l.notes,''),
       l.converted_customer_id, l.converted_order_id, l.converted_at,
       l.onboarding_schema_id, COALESCE(l.sales_type_at_create,''),
       l.created_by, l.created_at, l.updated_at,
       COALESCE(l.lead_type,'broadband') AS lead_type,
       l.referrer_customer_id,
       COALESCE(rc.full_name,'')  AS referrer_name,
       COALESCE(p.name,'')        AS product_name,
       COALESCE(p.code,'')        AS product_code,
       COALESCE(b.name,'')        AS branch_name,
       COALESCE(b.code,'')        AS branch_code,
       COALESCE(u.full_name,'')   AS sales_name
FROM crm.leads l
LEFT JOIN crm.products p          ON p.id = l.product_id
LEFT JOIN identity.branches b     ON b.id = l.branch_id
LEFT JOIN identity.users u        ON u.id = l.sales_id
LEFT JOIN crm.customers rc        ON rc.id = l.referrer_customer_id
`

// Create inserts a lead and seeds its document checklist in one transaction.
// We don't bother with savepoints — failing here means the request fails
// and the user retries (idempotent on lead_number via UNIQUE).
func (r *LeadRepository) Create(ctx context.Context, l *domain.Lead, docs []domain.OrderDocument) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var verdict any
	if l.CoverageVerdict != nil {
		verdict = string(*l.CoverageVerdict)
	}
	snapshot := l.CoverageSnapshot
	if len(snapshot) == 0 {
		snapshot = []byte("{}")
	}

	encNIK, sealErr := r.sealNIK(l.NIK)
	if sealErr != nil {
		return sealErr
	}
	leadTypeVal := string(l.LeadType)
	if leadTypeVal == "" {
		leadTypeVal = string(domain.LeadTypeBroadband)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO crm.leads (
			id, lead_number, status,
			full_name, phone, email, nik_encrypted,
			address, gps_lat, gps_lng,
			coverage_verdict, coverage_snapshot,
			accept_excess_cable, nearest_node_id, cable_distance_m, excess_charge,
			branch_id, product_id, sales_id, source, notes,
			onboarding_schema_id, sales_type_at_create,
			lead_type, referrer_customer_id,
			created_by, created_at, updated_at
		) VALUES (
			$1,$2,$3,
			$4,$5,$6,$7,
			$8,$9,$10,
			$11,$12::jsonb,
			$13,$14,$15,$16,
			$17,$18,$19,$20,$21,
			$22,$23,
			$24,$25,
			$26,$27,$27
		)
	`,
		l.ID, l.LeadNumber, string(l.Status),
		l.FullName, l.Phone, nullableString(l.Email), encNIK,
		l.Address, l.GPSLat, l.GPSLng,
		verdict, snapshot,
		l.AcceptExcessCable, l.NearestNodeID, l.CableDistanceM, l.ExcessCharge,
		l.BranchID, l.ProductID, l.SalesID, string(l.Source), nullableString(l.Notes),
		l.OnboardingSchemaID, nullableString(l.SalesTypeAtCreate),
		leadTypeVal, l.ReferrerCustomerID,
		l.CreatedBy, l.CreatedAt,
	); err != nil {
		return mapDBError(err, "lead.create", "create lead")
	}

	for _, d := range docs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO crm.order_documents (
				id, lead_id, doc_key, label, required, submitted,
				file_url, notes, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		`,
			d.ID, d.LeadID, d.DocKey, d.Label, d.Required, d.Submitted,
			nullableString(d.FileURL), nullableString(d.Notes), d.CreatedAt,
		); err != nil {
			return mapDBError(err, "doc.create", "create lead document")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

// Update writes back a (mostly) full lead. The usecase has already merged
// in partial-update logic — here we just replace the writable columns.
func (r *LeadRepository) Update(ctx context.Context, l *domain.Lead) error {
	var verdict any
	if l.CoverageVerdict != nil {
		verdict = string(*l.CoverageVerdict)
	}
	snapshot := l.CoverageSnapshot
	if len(snapshot) == 0 {
		snapshot = []byte("{}")
	}
	encNIK, sealErr := r.sealNIK(l.NIK)
	if sealErr != nil {
		return sealErr
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE crm.leads SET
		   status = $2,
		   full_name = $3, phone = $4, email = $5,
		   nik_encrypted = $6,
		   address = $7, gps_lat = $8, gps_lng = $9,
		   coverage_verdict = $10, coverage_snapshot = $11::jsonb,
		   accept_excess_cable = $12, nearest_node_id = $13,
		   cable_distance_m = $14, excess_charge = $15,
		   branch_id = $16, product_id = $17, sales_id = $18,
		   notes = $19, updated_at = NOW()
		 WHERE id = $1
	`,
		l.ID, string(l.Status),
		l.FullName, l.Phone, nullableString(l.Email),
		encNIK,
		l.Address, l.GPSLat, l.GPSLng,
		verdict, snapshot,
		l.AcceptExcessCable, l.NearestNodeID,
		l.CableDistanceM, l.ExcessCharge,
		l.BranchID, l.ProductID, l.SalesID,
		nullableString(l.Notes),
	)
	if err != nil {
		return mapDBError(err, "lead.update", "update lead")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("lead.not_found", "lead not found")
	}
	return nil
}

func (r *LeadRepository) List(ctx context.Context, f port.LeadListFilter) ([]port.LeadWithDocs, int, error) {
	var (
		args  []any
		conds []string
	)
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "l.status = $"+itoa(len(args)))
	}
	if f.BranchID != nil {
		args = append(args, *f.BranchID)
		conds = append(conds, "l.branch_id = $"+itoa(len(args)))
	}
	if f.SalesID != nil {
		args = append(args, *f.SalesID)
		conds = append(conds, "l.sales_id = $"+itoa(len(args)))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+s+"%")
		conds = append(conds, "(l.full_name ILIKE $"+itoa(len(args))+" OR l.phone ILIKE $"+itoa(len(args))+" OR l.lead_number ILIKE $"+itoa(len(args))+")")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM crm.leads l"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.lead_count", "count leads", err)
	}

	sql := leadSelect + where + " ORDER BY l.created_at DESC"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		sql += " LIMIT $" + itoa(len(args))
	}
	if f.Offset > 0 {
		args = append(args, f.Offset)
		sql += " OFFSET $" + itoa(len(args))
	}
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.lead_list", "list leads", err)
	}
	defer rows.Close()
	out := []port.LeadWithDocs{}
	for rows.Next() {
		lw, err := r.scanLeadWithDocs(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *lw)
	}
	return out, total, nil
}

func (r *LeadRepository) FindByID(ctx context.Context, id uuid.UUID) (*port.LeadWithDocs, error) {
	row := r.pool.QueryRow(ctx, leadSelect+" WHERE l.id = $1", id)
	lw, err := r.scanLeadWithDocs(row)
	if err != nil {
		return nil, err
	}
	docs, err := r.docsForLead(ctx, id)
	if err != nil {
		return nil, err
	}
	lw.Documents = docs
	return lw, nil
}

func (r *LeadRepository) MarkConverted(ctx context.Context, leadID, customerID, orderID uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE crm.leads
		   SET status = 'converted',
		       converted_customer_id = $2,
		       converted_order_id = $3,
		       converted_at = $4,
		       updated_at = NOW()
		 WHERE id = $1
	`, leadID, customerID, orderID, at)
	if err != nil {
		return mapDBError(err, "lead.mark_converted", "mark lead converted")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("lead.not_found", "lead not found")
	}
	return nil
}

func (r *LeadRepository) docsForLead(ctx context.Context, leadID uuid.UUID) ([]domain.OrderDocument, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, lead_id, doc_key, label, required, submitted,
		       COALESCE(file_url,''), COALESCE(notes,''), created_at, updated_at
		  FROM crm.order_documents
		 WHERE lead_id = $1
		 ORDER BY required DESC, doc_key
	`, leadID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.doc_list", "list docs", err)
	}
	defer rows.Close()
	out := []domain.OrderDocument{}
	for rows.Next() {
		var d domain.OrderDocument
		if err := rows.Scan(&d.ID, &d.LeadID, &d.DocKey, &d.Label, &d.Required,
			&d.Submitted, &d.FileURL, &d.Notes, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.doc_scan", "scan doc", err)
		}
		out = append(out, d)
	}
	return out, nil
}

// scanLeadWithDocs reads one row from leadSelect.
func (r *LeadRepository) scanLeadWithDocs(row pgx.Row) (*port.LeadWithDocs, error) {
	var (
		l       domain.Lead
		status  string
		verdict *string
		source  string
		encNIK  []byte
		out     port.LeadWithDocs
	)
	var leadTypeStr string
	err := row.Scan(
		&l.ID, &l.LeadNumber, &status,
		&l.FullName, &l.Phone, &l.Email, &encNIK,
		&l.Address, &l.GPSLat, &l.GPSLng,
		&verdict, &l.CoverageSnapshot,
		&l.AcceptExcessCable, &l.NearestNodeID, &l.CableDistanceM, &l.ExcessCharge,
		&l.BranchID, &l.ProductID, &l.SalesID, &source, &l.Notes,
		&l.ConvertedCustomerID, &l.ConvertedOrderID, &l.ConvertedAt,
		&l.OnboardingSchemaID, &l.SalesTypeAtCreate,
		&l.CreatedBy, &l.CreatedAt, &l.UpdatedAt,
		&leadTypeStr, &l.ReferrerCustomerID,
		&out.ReferrerName,
		&out.ProductName, &out.ProductCode,
		&out.BranchName, &out.BranchCode, &out.SalesName,
	)
	if len(encNIK) > 0 && r.sealer != nil {
		if dec, err := r.sealer.Open(encNIK); err == nil {
			l.NIK = dec
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("lead.not_found", "lead not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.lead_scan", "scan lead", err)
	}
	l.Status = domain.LeadStatus(status)
	l.Source = domain.LeadSource(source)
	l.LeadType = domain.LeadType(leadTypeStr)
	if verdict != nil {
		v := domain.CoverageVerdict(*verdict)
		l.CoverageVerdict = &v
	}
	out.Lead = l
	return &out, nil
}
