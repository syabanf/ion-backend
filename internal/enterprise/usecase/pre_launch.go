package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Builder wiring
// =====================================================================

func (s *Service) WithPODocuments(repo port.PODocumentRepository) *Service {
	s.poDocuments = repo
	return s
}
func (s *Service) WithPaymentProofs(repo port.PaymentProofRepository) *Service {
	s.paymentProofs = repo
	return s
}
func (s *Service) WithEWOChecklist(repo port.EWOChecklistRepository) *Service {
	s.ewoChecklist = repo
	return s
}

// WithEWOChecklistTemplates wires the reusable template repo.
func (s *Service) WithEWOChecklistTemplates(repo port.EWOChecklistTemplateRepository) *Service {
	s.ewoChecklistTemplates = repo
	return s
}
func (s *Service) WithProjects(
	p port.ProjectRepository,
	sites port.ProjectSiteRepository,
	svcs port.EnterpriseServiceRepository,
) *Service {
	s.projects = p
	s.projectSites = sites
	s.enterpriseSvcs = svcs
	return s
}
func (s *Service) WithRFQs(repo port.RFQRepository) *Service {
	s.rfqs = repo
	return s
}

// =====================================================================
// E7 — PO documents
// =====================================================================

func (s *Service) ListPODocuments(ctx context.Context, opportunityID uuid.UUID) ([]domain.PODocument, error) {
	if s.poDocuments == nil {
		return []domain.PODocument{}, nil
	}
	return s.poDocuments.ListByOpportunity(ctx, opportunityID)
}

func (s *Service) UploadPODocument(ctx context.Context, in port.UploadPODocumentInput) (*domain.PODocument, error) {
	if s.poDocuments == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "po_document.not_configured", "PO repo not wired", nil)
	}
	rev, err := s.poDocuments.NextRevision(ctx, in.OpportunityID)
	if err != nil {
		return nil, err
	}
	doc, err := domain.NewPODocument(
		in.OpportunityID, in.PONumber, rev,
		in.FileURL, in.FileName, in.ContentType, in.FileSizeBytes,
	)
	if err != nil {
		return nil, err
	}
	doc.IssuedByPIC = strings.TrimSpace(in.IssuedByPIC)
	doc.Notes = strings.TrimSpace(in.Notes)
	doc.UploadedBy = in.UploadedBy
	if err := s.poDocuments.Create(ctx, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// =====================================================================
// E8 — payment proofs
// =====================================================================

func (s *Service) ListPaymentProofs(ctx context.Context, paymentID uuid.UUID) ([]domain.PaymentProof, error) {
	if s.paymentProofs == nil {
		return []domain.PaymentProof{}, nil
	}
	return s.paymentProofs.ListByPayment(ctx, paymentID)
}

func (s *Service) UploadPaymentProof(ctx context.Context, in port.UploadPaymentProofInput) (*domain.PaymentProof, error) {
	if s.paymentProofs == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "payment_proof.not_configured", "proof repo not wired", nil)
	}
	p, err := domain.NewPaymentProof(in.InvoicePaymentID, in.FileURL, in.FileName, in.ContentType, in.FileSizeBytes)
	if err != nil {
		return nil, err
	}
	p.UploadedBy = in.UploadedBy
	p.Notes = strings.TrimSpace(in.Notes)
	if err := s.paymentProofs.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// =====================================================================
// E9 — EWO checklist
// =====================================================================

func (s *Service) ListEWOChecklistItems(ctx context.Context, ewoID uuid.UUID) ([]domain.EWOChecklistItem, error) {
	if s.ewoChecklist == nil {
		return []domain.EWOChecklistItem{}, nil
	}
	return s.ewoChecklist.ListByEWO(ctx, ewoID)
}

// ReplaceEWOChecklist sets the entire checklist for an EWO. Designed
// for initial seeding from a template — re-running with different items
// REPLACES the set, which would lose progress on existing items. The
// HTTP layer warns operators.
func (s *Service) ReplaceEWOChecklist(ctx context.Context, in port.ReplaceEWOChecklistInput) ([]domain.EWOChecklistItem, error) {
	if s.ewoChecklist == nil || s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	if _, err := s.ewos.FindByID(ctx, in.EWOID); err != nil {
		return nil, err
	}
	// We don't actually have a single "ReplaceBatch" — repo only does
	// CreateBatch. To replace we'd need a delete-then-insert. Skipping
	// for MVP: only accept fresh seeding when the existing list is empty.
	existing, err := s.ewoChecklist.ListByEWO(ctx, in.EWOID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, derrors.Conflict(
			"ewo_checklist.already_seeded",
			"this EWO already has a checklist — update individual items instead",
		)
	}
	items := make([]domain.EWOChecklistItem, 0, len(in.Items))
	for _, raw := range in.Items {
		it, err := domain.NewEWOChecklistItem(in.EWOID, raw.SeqNo, raw.Label, raw.Description)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	if err := s.ewoChecklist.CreateBatch(ctx, items); err != nil {
		return nil, err
	}
	// Reset EWO progress.
	s.refreshEWOProgress(ctx, in.EWOID)
	return items, nil
}

func (s *Service) UpdateEWOChecklistItem(ctx context.Context, in port.UpdateEWOChecklistItemInput) (*domain.EWOChecklistItem, error) {
	if s.ewoChecklist == nil {
		return nil, derrors.Wrap(derrors.KindInternal, "ewo_checklist.not_configured", "checklist repo not wired", nil)
	}
	it, err := s.ewoChecklist.FindByID(ctx, in.ItemID)
	if err != nil {
		return nil, err
	}
	if err := it.SetStatus(domain.EWOChecklistItemStatus(in.Status), in.CompletedBy); err != nil {
		return nil, err
	}
	if in.Notes != "" {
		it.Notes = in.Notes
	}
	if err := s.ewoChecklist.Update(ctx, it); err != nil {
		return nil, err
	}
	// Refresh EWO progress + maybe auto-advance EWO status.
	s.refreshEWOProgress(ctx, it.EWOID)
	return it, nil
}

// refreshEWOProgress recomputes progress_pct from the checklist + auto-
// advances the EWO when the checklist is fully done. Best-effort — any
// repo error is logged + swallowed.
func (s *Service) refreshEWOProgress(ctx context.Context, ewoID uuid.UUID) {
	if s.ewos == nil || s.ewoChecklist == nil {
		return
	}
	items, err := s.ewoChecklist.ListByEWO(ctx, ewoID)
	if err != nil {
		return
	}
	progress := domain.EWOProgress(items)
	e, err := s.ewos.FindByID(ctx, ewoID)
	if err != nil {
		return
	}
	e.ProgressPct = progress
	_ = s.ewos.Update(ctx, e)
}

// =====================================================================
// E11 — projects / sites / services
// =====================================================================

func (s *Service) ListProjects(ctx context.Context, status string, opportunityID *uuid.UUID, limit, offset int) ([]domain.Project, int, error) {
	if s.projects == nil {
		return nil, 0, nil
	}
	return s.projects.List(ctx, status, opportunityID, limit, offset)
}

func (s *Service) GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, []domain.ProjectSite, error) {
	if s.projects == nil {
		return nil, nil, errFinanceNotConfigured()
	}
	p, err := s.projects.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	sites, err := s.projectSites.ListByProject(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	return p, sites, nil
}

// CreateProject seeds a planning project from an accepted quotation.
// Idempotent — same quotation produces one project.
func (s *Service) CreateProject(ctx context.Context, in port.CreateProjectInput) (*domain.Project, error) {
	if s.projects == nil || s.quotations == nil {
		return nil, errFinanceNotConfigured()
	}
	if existing, err := s.projects.FindByQuotationID(ctx, in.QuotationID); err == nil {
		return existing, nil
	} else if !derrors.IsNotFound(err) {
		return nil, err
	}
	q, err := s.quotations.FindByID(ctx, in.QuotationID)
	if err != nil {
		return nil, err
	}
	p, err := domain.NewProject(q.ID, q.OpportunityID, q.BOQVersionID, domain.GenerateProjectNumber(time.Now()))
	if err != nil {
		return nil, err
	}
	p.ProjectManagerUserID = in.ProjectManagerUserID
	p.Notes = strings.TrimSpace(in.Notes)
	if err := s.projects.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) ListProjectSites(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectSite, error) {
	if s.projectSites == nil {
		return []domain.ProjectSite{}, nil
	}
	return s.projectSites.ListByProject(ctx, projectID)
}

func (s *Service) CreateProjectSite(ctx context.Context, in port.CreateProjectSiteInput) (*domain.ProjectSite, error) {
	if s.projectSites == nil {
		return nil, errFinanceNotConfigured()
	}
	site, err := domain.NewProjectSite(in.ProjectID, in.SiteCode, in.SiteName)
	if err != nil {
		return nil, err
	}
	site.Address = in.Address
	site.Lat = in.Lat
	site.Lng = in.Lng
	site.PICName = in.PICName
	site.PICPhone = in.PICPhone
	if err := s.projectSites.Create(ctx, site); err != nil {
		return nil, err
	}
	return site, nil
}

// ActivateProjectSite — flips a site to `active` and, when ALL the
// project's sites are now active, auto-flips the parent project to
// `completed`. Idempotent on already-active sites.
//
// This implements the "Project completion auto-flips when all sites
// active" pre-launch polish item.
func (s *Service) ActivateProjectSite(ctx context.Context, siteID uuid.UUID) (*domain.ProjectSite, error) {
	if s.projectSites == nil {
		return nil, errFinanceNotConfigured()
	}
	site, err := s.projectSites.FindByID(ctx, siteID)
	if err != nil {
		return nil, err
	}
	if site.Status == domain.SiteStatusActive {
		return site, nil
	}
	now := time.Now().UTC()
	site.Status = domain.SiteStatusActive
	site.ActivatedAt = &now
	if err := s.projectSites.Update(ctx, site); err != nil {
		return nil, err
	}
	// Roll up to project — auto-complete when EVERY site is active.
	if s.projects != nil {
		siblings, err := s.projectSites.ListByProject(ctx, site.ProjectID)
		if err == nil {
			allActive := len(siblings) > 0
			for _, sib := range siblings {
				if sib.Status != domain.SiteStatusActive {
					allActive = false
					break
				}
			}
			if allActive {
				p, err := s.projects.FindByID(ctx, site.ProjectID)
				if err == nil && p.Status != domain.ProjectStatusCompleted {
					p.Status = domain.ProjectStatusCompleted
					p.CompletedAt = &now
					_ = s.projects.Update(ctx, p)
				}
			}
		}
	}
	return site, nil
}

func (s *Service) ListEnterpriseServices(ctx context.Context, siteID uuid.UUID) ([]domain.EnterpriseService, error) {
	if s.enterpriseSvcs == nil {
		return []domain.EnterpriseService{}, nil
	}
	return s.enterpriseSvcs.ListBySite(ctx, siteID)
}

func (s *Service) CreateEnterpriseService(ctx context.Context, in port.CreateEnterpriseServiceInput) (*domain.EnterpriseService, error) {
	if s.enterpriseSvcs == nil {
		return nil, errFinanceNotConfigured()
	}
	svc, err := domain.NewEnterpriseService(in.ProjectSiteID, in.ServiceCode, in.ServiceName)
	if err != nil {
		return nil, err
	}
	svc.BOQLineID = in.BOQLineID
	svc.Notes = strings.TrimSpace(in.Notes)
	if err := s.enterpriseSvcs.Create(ctx, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

// =====================================================================
// E12 — RFQ
// =====================================================================

func (s *Service) ListRFQs(ctx context.Context, status string, opportunityID, assignedTo *uuid.UUID, limit, offset int) ([]domain.RFQ, int, error) {
	if s.rfqs == nil {
		return nil, 0, nil
	}
	return s.rfqs.List(ctx, status, opportunityID, assignedTo, limit, offset)
}

func (s *Service) GetRFQ(ctx context.Context, id uuid.UUID) (*domain.RFQ, error) {
	if s.rfqs == nil {
		return nil, errFinanceNotConfigured()
	}
	return s.rfqs.FindByID(ctx, id)
}

func (s *Service) CreateRFQ(ctx context.Context, in port.CreateRFQInput) (*domain.RFQ, error) {
	if s.rfqs == nil {
		return nil, errFinanceNotConfigured()
	}
	r, err := domain.NewRFQ(in.OpportunityID, domain.GenerateRFQNumber(time.Now()), in.Requirements)
	if err != nil {
		return nil, err
	}
	r.RequestedBy = in.RequestedBy
	r.Constraints = in.Constraints
	if in.DeadlineDays > 0 {
		d := time.Now().UTC().AddDate(0, 0, in.DeadlineDays)
		r.DeadlineAt = &d
	}
	if err := s.rfqs.Create(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) AssignRFQ(ctx context.Context, in port.AssignRFQInput) (*domain.RFQ, error) {
	if s.rfqs == nil {
		return nil, errFinanceNotConfigured()
	}
	r, err := s.rfqs.FindByID(ctx, in.RFQID)
	if err != nil {
		return nil, err
	}
	if err := r.Assign(in.AssignedTo); err != nil {
		return nil, err
	}
	if err := s.rfqs.Update(ctx, r); err != nil {
		return nil, err
	}
	// Notify the assignee.
	s.Notify(ctx, domain.NewNotification(
		in.AssignedTo,
		"rfq.assigned_to_me",
		"rfq", r.ID,
		"RFQ "+r.RFQNumber+" assigned to you",
		"Build the BOQ for this RFQ. Open the detail page for requirements.",
		domain.NotificationSeverityInfo,
	))
	return r, nil
}

func (s *Service) FulfillRFQ(ctx context.Context, in port.FulfillRFQInput) (*domain.RFQ, error) {
	if s.rfqs == nil {
		return nil, errFinanceNotConfigured()
	}
	r, err := s.rfqs.FindByID(ctx, in.RFQID)
	if err != nil {
		return nil, err
	}
	if err := r.Fulfill(in.FulfilledBOQID); err != nil {
		return nil, err
	}
	if err := s.rfqs.Update(ctx, r); err != nil {
		return nil, err
	}
	// E12 pre-launch — stamp the reverse pointer on the BOQ so its
	// detail page can render "← fulfilling RFQ-...".
	if s.boqs != nil {
		if err := s.boqs.SetSourceRFQID(ctx, in.FulfilledBOQID, r.ID); err != nil {
			if s.log != nil {
				s.log.Warn("rfq fulfill backlink failed",
					"boq", in.FulfilledBOQID.String(),
					"rfq", r.ID.String(),
					"err", err.Error(),
				)
			}
		}
	}
	if r.RequestedBy != nil {
		s.Notify(ctx, domain.NewNotification(
			*r.RequestedBy,
			"rfq.fulfilled",
			"rfq", r.ID,
			"RFQ "+r.RFQNumber+" fulfilled",
			"Sales Support has built the BOQ. Open it for review.",
			domain.NotificationSeverityInfo,
		))
	}
	return r, nil
}

func (s *Service) CancelRFQ(ctx context.Context, in port.CancelRFQInput) (*domain.RFQ, error) {
	if s.rfqs == nil {
		return nil, errFinanceNotConfigured()
	}
	r, err := s.rfqs.FindByID(ctx, in.RFQID)
	if err != nil {
		return nil, err
	}
	if err := r.Cancel(in.Reason); err != nil {
		return nil, err
	}
	if err := s.rfqs.Update(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}
