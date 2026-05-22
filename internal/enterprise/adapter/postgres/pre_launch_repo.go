package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// E7 — PODocumentRepository
// =====================================================================

type PODocumentRepository struct {
	pool *pgxpool.Pool
}

func NewPODocumentRepository(pool *pgxpool.Pool) *PODocumentRepository {
	return &PODocumentRepository{pool: pool}
}

var _ port.PODocumentRepository = (*PODocumentRepository)(nil)

const poDocCols = `
	id, opportunity_id, po_number, po_revision,
	file_url, file_name, file_size_bytes, content_type,
	COALESCE(issued_by_pic, ''), received_at, uploaded_by,
	COALESCE(notes, ''), created_at
`

func (r *PODocumentRepository) ListByOpportunity(ctx context.Context, opp uuid.UUID) ([]domain.PODocument, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+poDocCols+` FROM enterprise.po_documents WHERE opportunity_id = $1 ORDER BY po_revision DESC`,
		opp,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.po_documents_list", "list", err)
	}
	defer rows.Close()
	out := []domain.PODocument{}
	for rows.Next() {
		d, err := scanPODocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *PODocumentRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PODocument, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+poDocCols+` FROM enterprise.po_documents WHERE id = $1`, id)
	d, err := scanPODocument(row)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *PODocumentRepository) NextRevision(ctx context.Context, opp uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(po_revision), 0) + 1 FROM enterprise.po_documents WHERE opportunity_id = $1`,
		opp,
	).Scan(&n)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.po_documents_next_rev", "next revision", err)
	}
	return n, nil
}

func (r *PODocumentRepository) Create(ctx context.Context, d *domain.PODocument) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.po_documents
			(id, opportunity_id, po_number, po_revision,
			 file_url, file_name, file_size_bytes, content_type,
			 issued_by_pic, received_at, uploaded_by, notes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		d.ID, d.OpportunityID, d.PONumber, d.PORevision,
		d.FileURL, d.FileName, d.FileSizeBytes, d.ContentType,
		d.IssuedByPIC, d.ReceivedAt, d.UploadedBy, d.Notes, d.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "po_document", "insert")
	}
	return nil
}

func scanPODocument(row pgx.Row) (domain.PODocument, error) {
	var d domain.PODocument
	err := row.Scan(
		&d.ID, &d.OpportunityID, &d.PONumber, &d.PORevision,
		&d.FileURL, &d.FileName, &d.FileSizeBytes, &d.ContentType,
		&d.IssuedByPIC, &d.ReceivedAt, &d.UploadedBy,
		&d.Notes, &d.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PODocument{}, derrors.NotFound("po_document.not_found", "PO document not found")
	}
	if err != nil {
		return domain.PODocument{}, derrors.Wrap(derrors.KindInternal, "db.po_document_scan", "scan", err)
	}
	return d, nil
}

// =====================================================================
// E8 — PaymentProofRepository
// =====================================================================

type PaymentProofRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentProofRepository(pool *pgxpool.Pool) *PaymentProofRepository {
	return &PaymentProofRepository{pool: pool}
}

var _ port.PaymentProofRepository = (*PaymentProofRepository)(nil)

func (r *PaymentProofRepository) ListByPayment(ctx context.Context, paymentID uuid.UUID) ([]domain.PaymentProof, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, invoice_payment_id, file_url, file_name, file_size_bytes,
		       content_type, uploaded_by, COALESCE(notes, ''), created_at
		FROM enterprise.payment_proofs
		WHERE invoice_payment_id = $1
		ORDER BY created_at DESC
	`, paymentID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.payment_proofs_list", "list", err)
	}
	defer rows.Close()
	out := []domain.PaymentProof{}
	for rows.Next() {
		var p domain.PaymentProof
		if err := rows.Scan(
			&p.ID, &p.InvoicePaymentID, &p.FileURL, &p.FileName, &p.FileSizeBytes,
			&p.ContentType, &p.UploadedBy, &p.Notes, &p.CreatedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.payment_proof_scan", "scan", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *PaymentProofRepository) Create(ctx context.Context, p *domain.PaymentProof) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.payment_proofs
			(id, invoice_payment_id, file_url, file_name, file_size_bytes,
			 content_type, uploaded_by, notes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		p.ID, p.InvoicePaymentID, p.FileURL, p.FileName, p.FileSizeBytes,
		p.ContentType, p.UploadedBy, p.Notes, p.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "payment_proof", "insert")
	}
	return nil
}

// =====================================================================
// E9 — EWOChecklistRepository
// =====================================================================

type EWOChecklistRepository struct {
	pool *pgxpool.Pool
}

func NewEWOChecklistRepository(pool *pgxpool.Pool) *EWOChecklistRepository {
	return &EWOChecklistRepository{pool: pool}
}

var _ port.EWOChecklistRepository = (*EWOChecklistRepository)(nil)

const ewoChecklistCols = `
	id, ewo_id, seq_no, label, COALESCE(description, ''),
	status, completed_at, completed_by, COALESCE(notes, ''),
	created_at, updated_at
`

func (r *EWOChecklistRepository) ListByEWO(ctx context.Context, ewoID uuid.UUID) ([]domain.EWOChecklistItem, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+ewoChecklistCols+` FROM enterprise.ewo_checklist_items WHERE ewo_id = $1 ORDER BY seq_no`,
		ewoID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.ewo_checklist_list", "list", err)
	}
	defer rows.Close()
	out := []domain.EWOChecklistItem{}
	for rows.Next() {
		it, err := scanEWOChecklistItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (r *EWOChecklistRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.EWOChecklistItem, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ewoChecklistCols+` FROM enterprise.ewo_checklist_items WHERE id = $1`, id)
	it, err := scanEWOChecklistItem(row)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (r *EWOChecklistRepository) CreateBatch(ctx context.Context, items []domain.EWOChecklistItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.ewo_checklist_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.ewo_checklist_items
				(id, ewo_id, seq_no, label, description, status,
				 completed_at, completed_by, notes, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`,
			it.ID, it.EWOID, it.SeqNo, it.Label, it.Description, string(it.Status),
			it.CompletedAt, it.CompletedBy, it.Notes, it.CreatedAt, it.UpdatedAt,
		); err != nil {
			return mapDBError(err, "ewo_checklist_item", "insert")
		}
	}
	return tx.Commit(ctx)
}

func (r *EWOChecklistRepository) Update(ctx context.Context, it *domain.EWOChecklistItem) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.ewo_checklist_items
		SET status = $2, completed_at = $3, completed_by = $4, notes = $5, updated_at = NOW()
		WHERE id = $1
	`,
		it.ID, string(it.Status), it.CompletedAt, it.CompletedBy, it.Notes,
	)
	if err != nil {
		return mapDBError(err, "ewo_checklist_item", "update")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("ewo_checklist_item.not_found", "checklist item not found")
	}
	return nil
}

func scanEWOChecklistItem(row pgx.Row) (domain.EWOChecklistItem, error) {
	var (
		it     domain.EWOChecklistItem
		status string
	)
	err := row.Scan(
		&it.ID, &it.EWOID, &it.SeqNo, &it.Label, &it.Description,
		&status, &it.CompletedAt, &it.CompletedBy, &it.Notes,
		&it.CreatedAt, &it.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EWOChecklistItem{}, derrors.NotFound("ewo_checklist_item.not_found", "checklist item not found")
	}
	if err != nil {
		return domain.EWOChecklistItem{}, derrors.Wrap(derrors.KindInternal, "db.ewo_checklist_scan", "scan", err)
	}
	it.Status = domain.EWOChecklistItemStatus(status)
	return it, nil
}

// =====================================================================
// E11 — Projects, Sites, Services
// =====================================================================

type ProjectRepository struct {
	pool *pgxpool.Pool
}

func NewProjectRepository(pool *pgxpool.Pool) *ProjectRepository {
	return &ProjectRepository{pool: pool}
}

var _ port.ProjectRepository = (*ProjectRepository)(nil)

const projectCols = `
	id, project_number, quotation_id, opportunity_id, boq_version_id,
	status, started_at, completed_at, cancelled_at,
	COALESCE(cancel_reason, ''), project_manager_user_id,
	COALESCE(notes, ''), revision, created_at, updated_at
`

func (r *ProjectRepository) List(ctx context.Context, status string, opportunityID *uuid.UUID, limit, offset int) ([]domain.Project, int, error) {
	var wh []string
	var args []any
	if status != "" {
		args = append(args, status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if opportunityID != nil {
		args = append(args, *opportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	if limit <= 0 {
		limit = 50
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.projects`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.project_count", "count", err)
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + projectCols + ` FROM enterprise.projects` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.project_list", "list", err)
	}
	defer rows.Close()
	out := []domain.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, nil
}

func (r *ProjectRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+projectCols+` FROM enterprise.projects WHERE id = $1`, id)
	p, err := scanProject(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ProjectRepository) FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.Project, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+projectCols+` FROM enterprise.projects WHERE quotation_id = $1`, quotationID)
	p, err := scanProject(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ProjectRepository) Create(ctx context.Context, p *domain.Project) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.projects
			(id, project_number, quotation_id, opportunity_id, boq_version_id,
			 status, started_at, completed_at, cancelled_at, cancel_reason,
			 project_manager_user_id, notes, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`,
		p.ID, p.ProjectNumber, p.QuotationID, p.OpportunityID, p.BOQVersionID,
		string(p.Status), p.StartedAt, p.CompletedAt, p.CancelledAt, p.CancelReason,
		p.ProjectManagerUserID, p.Notes, p.Revision, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "project", "insert")
	}
	return nil
}

func (r *ProjectRepository) Update(ctx context.Context, p *domain.Project) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.projects
		SET status = $2, started_at = $3, completed_at = $4, cancelled_at = $5,
		    cancel_reason = $6, project_manager_user_id = $7, notes = $8,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`,
		p.ID, string(p.Status), p.StartedAt, p.CompletedAt, p.CancelledAt,
		p.CancelReason, p.ProjectManagerUserID, p.Notes,
	)
	if err != nil {
		return mapDBError(err, "project", "update")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("project.not_found", "project not found")
	}
	return nil
}

func scanProject(row pgx.Row) (domain.Project, error) {
	var (
		p      domain.Project
		status string
	)
	err := row.Scan(
		&p.ID, &p.ProjectNumber, &p.QuotationID, &p.OpportunityID, &p.BOQVersionID,
		&status, &p.StartedAt, &p.CompletedAt, &p.CancelledAt,
		&p.CancelReason, &p.ProjectManagerUserID,
		&p.Notes, &p.Revision, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, derrors.NotFound("project.not_found", "project not found")
	}
	if err != nil {
		return domain.Project{}, derrors.Wrap(derrors.KindInternal, "db.project_scan", "scan", err)
	}
	p.Status = domain.ProjectStatus(status)
	return p, nil
}

type ProjectSiteRepository struct {
	pool *pgxpool.Pool
}

func NewProjectSiteRepository(pool *pgxpool.Pool) *ProjectSiteRepository {
	return &ProjectSiteRepository{pool: pool}
}

var _ port.ProjectSiteRepository = (*ProjectSiteRepository)(nil)

const projectSiteCols = `
	id, project_id, site_code, site_name,
	COALESCE(address, ''), lat, lng,
	COALESCE(pic_name, ''), COALESCE(pic_phone, ''),
	status, activated_at, created_at, updated_at
`

func (r *ProjectSiteRepository) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectSite, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+projectSiteCols+` FROM enterprise.project_sites WHERE project_id = $1 ORDER BY site_code`,
		projectID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.project_sites_list", "list", err)
	}
	defer rows.Close()
	out := []domain.ProjectSite{}
	for rows.Next() {
		s, err := scanProjectSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *ProjectSiteRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ProjectSite, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+projectSiteCols+` FROM enterprise.project_sites WHERE id = $1`, id)
	s, err := scanProjectSite(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *ProjectSiteRepository) Create(ctx context.Context, s *domain.ProjectSite) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.project_sites
			(id, project_id, site_code, site_name, address, lat, lng,
			 pic_name, pic_phone, status, activated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		s.ID, s.ProjectID, s.SiteCode, s.SiteName, s.Address, s.Lat, s.Lng,
		s.PICName, s.PICPhone, string(s.Status), s.ActivatedAt, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "project_site", "insert")
	}
	return nil
}

func (r *ProjectSiteRepository) Update(ctx context.Context, s *domain.ProjectSite) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.project_sites
		SET site_name = $2, address = $3, lat = $4, lng = $5,
		    pic_name = $6, pic_phone = $7, status = $8, activated_at = $9,
		    updated_at = NOW()
		WHERE id = $1
	`,
		s.ID, s.SiteName, s.Address, s.Lat, s.Lng,
		s.PICName, s.PICPhone, string(s.Status), s.ActivatedAt,
	)
	if err != nil {
		return mapDBError(err, "project_site", "update")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("project_site.not_found", "site not found")
	}
	return nil
}

func scanProjectSite(row pgx.Row) (domain.ProjectSite, error) {
	var (
		s      domain.ProjectSite
		status string
	)
	err := row.Scan(
		&s.ID, &s.ProjectID, &s.SiteCode, &s.SiteName,
		&s.Address, &s.Lat, &s.Lng,
		&s.PICName, &s.PICPhone, &status, &s.ActivatedAt,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ProjectSite{}, derrors.NotFound("project_site.not_found", "site not found")
	}
	if err != nil {
		return domain.ProjectSite{}, derrors.Wrap(derrors.KindInternal, "db.project_site_scan", "scan", err)
	}
	s.Status = domain.ProjectSiteStatus(status)
	return s, nil
}

type EnterpriseServiceRepository struct {
	pool *pgxpool.Pool
}

func NewEnterpriseServiceRepository(pool *pgxpool.Pool) *EnterpriseServiceRepository {
	return &EnterpriseServiceRepository{pool: pool}
}

var _ port.EnterpriseServiceRepository = (*EnterpriseServiceRepository)(nil)

const enterpriseServiceCols = `
	id, project_site_id, boq_line_id, service_code, service_name,
	status, activated_at, terminated_at, COALESCE(notes, ''),
	created_at, updated_at
`

func (r *EnterpriseServiceRepository) ListBySite(ctx context.Context, siteID uuid.UUID) ([]domain.EnterpriseService, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+enterpriseServiceCols+` FROM enterprise.enterprise_services WHERE project_site_id = $1 ORDER BY service_code`,
		siteID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.enterprise_services_list", "list", err)
	}
	defer rows.Close()
	out := []domain.EnterpriseService{}
	for rows.Next() {
		s, err := scanEnterpriseService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *EnterpriseServiceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.EnterpriseService, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+enterpriseServiceCols+` FROM enterprise.enterprise_services WHERE id = $1`, id)
	s, err := scanEnterpriseService(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *EnterpriseServiceRepository) Create(ctx context.Context, s *domain.EnterpriseService) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.enterprise_services
			(id, project_site_id, boq_line_id, service_code, service_name,
			 status, activated_at, terminated_at, notes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		s.ID, s.ProjectSiteID, s.BOQLineID, s.ServiceCode, s.ServiceName,
		string(s.Status), s.ActivatedAt, s.TerminatedAt, s.Notes,
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "enterprise_service", "insert")
	}
	return nil
}

func (r *EnterpriseServiceRepository) Update(ctx context.Context, s *domain.EnterpriseService) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.enterprise_services
		SET service_name = $2, status = $3, activated_at = $4, terminated_at = $5,
		    notes = $6, updated_at = NOW()
		WHERE id = $1
	`,
		s.ID, s.ServiceName, string(s.Status), s.ActivatedAt, s.TerminatedAt, s.Notes,
	)
	if err != nil {
		return mapDBError(err, "enterprise_service", "update")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("enterprise_service.not_found", "service not found")
	}
	return nil
}

func scanEnterpriseService(row pgx.Row) (domain.EnterpriseService, error) {
	var (
		s      domain.EnterpriseService
		status string
	)
	err := row.Scan(
		&s.ID, &s.ProjectSiteID, &s.BOQLineID, &s.ServiceCode, &s.ServiceName,
		&status, &s.ActivatedAt, &s.TerminatedAt, &s.Notes,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EnterpriseService{}, derrors.NotFound("enterprise_service.not_found", "service not found")
	}
	if err != nil {
		return domain.EnterpriseService{}, derrors.Wrap(derrors.KindInternal, "db.enterprise_service_scan", "scan", err)
	}
	s.Status = domain.EnterpriseServiceStatus(status)
	return s, nil
}

// =====================================================================
// E12 — RFQRepository
// =====================================================================

type RFQRepository struct {
	pool *pgxpool.Pool
}

func NewRFQRepository(pool *pgxpool.Pool) *RFQRepository {
	return &RFQRepository{pool: pool}
}

var _ port.RFQRepository = (*RFQRepository)(nil)

const rfqCols = `
	id, rfq_number, opportunity_id, status,
	requested_by, assigned_to,
	COALESCE(requirements, ''), COALESCE(constraints, ''),
	deadline_at, fulfilled_at, fulfilled_boq_id,
	cancelled_at, COALESCE(cancel_reason, ''),
	COALESCE(notes, ''), revision, created_at, updated_at
`

func (r *RFQRepository) List(ctx context.Context, status string, opportunityID, assignedTo *uuid.UUID, limit, offset int) ([]domain.RFQ, int, error) {
	var wh []string
	var args []any
	if status != "" {
		args = append(args, status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if opportunityID != nil {
		args = append(args, *opportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	if assignedTo != nil {
		args = append(args, *assignedTo)
		wh = append(wh, fmt.Sprintf("assigned_to = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	if limit <= 0 {
		limit = 50
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM enterprise.rfqs`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.rfq_count", "count", err)
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + rfqCols + ` FROM enterprise.rfqs` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.rfq_list", "list", err)
	}
	defer rows.Close()
	out := []domain.RFQ{}
	for rows.Next() {
		rfq, err := scanRFQ(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rfq)
	}
	return out, total, nil
}

func (r *RFQRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.RFQ, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+rfqCols+` FROM enterprise.rfqs WHERE id = $1`, id)
	rfq, err := scanRFQ(row)
	if err != nil {
		return nil, err
	}
	return &rfq, nil
}

func (r *RFQRepository) Create(ctx context.Context, rfq *domain.RFQ) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.rfqs
			(id, rfq_number, opportunity_id, status, requested_by, assigned_to,
			 requirements, constraints, deadline_at, fulfilled_at, fulfilled_boq_id,
			 cancelled_at, cancel_reason, notes, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`,
		rfq.ID, rfq.RFQNumber, rfq.OpportunityID, string(rfq.Status),
		rfq.RequestedBy, rfq.AssignedTo,
		rfq.Requirements, rfq.Constraints, rfq.DeadlineAt, rfq.FulfilledAt, rfq.FulfilledBOQID,
		rfq.CancelledAt, rfq.CancelReason, rfq.Notes, rfq.Revision,
		rfq.CreatedAt, rfq.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "rfq", "insert")
	}
	return nil
}

func (r *RFQRepository) Update(ctx context.Context, rfq *domain.RFQ) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.rfqs
		SET status = $2, assigned_to = $3, requirements = $4, constraints = $5,
		    deadline_at = $6, fulfilled_at = $7, fulfilled_boq_id = $8,
		    cancelled_at = $9, cancel_reason = $10, notes = $11,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`,
		rfq.ID, string(rfq.Status), rfq.AssignedTo, rfq.Requirements, rfq.Constraints,
		rfq.DeadlineAt, rfq.FulfilledAt, rfq.FulfilledBOQID,
		rfq.CancelledAt, rfq.CancelReason, rfq.Notes,
	)
	if err != nil {
		return mapDBError(err, "rfq", "update")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("rfq.not_found", "RFQ not found")
	}
	return nil
}

func scanRFQ(row pgx.Row) (domain.RFQ, error) {
	var (
		r      domain.RFQ
		status string
	)
	err := row.Scan(
		&r.ID, &r.RFQNumber, &r.OpportunityID, &status,
		&r.RequestedBy, &r.AssignedTo,
		&r.Requirements, &r.Constraints,
		&r.DeadlineAt, &r.FulfilledAt, &r.FulfilledBOQID,
		&r.CancelledAt, &r.CancelReason,
		&r.Notes, &r.Revision, &r.CreatedAt, &r.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RFQ{}, derrors.NotFound("rfq.not_found", "RFQ not found")
	}
	if err != nil {
		return domain.RFQ{}, derrors.Wrap(derrors.KindInternal, "db.rfq_scan", "scan", err)
	}
	r.Status = domain.RFQStatus(status)
	return r, nil
}
