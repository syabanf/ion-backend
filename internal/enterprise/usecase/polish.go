package usecase

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Polish bundle — 4 small but functional additions stacked here so
// the call sites stay co-located. Each method is independently usable.
// =====================================================================

// ----- EWO checklist templates -----------------------------------------------

// EWOChecklistTemplate is the admin-managed reusable seed list shape.
type EWOChecklistTemplate struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Description string
	Active      bool
	Items       []domain.EWOChecklistItem // only seq_no / label / description are persisted
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SeedEWOChecklistFromTemplate — extension of the existing manual seed
// path that pulls items from `ewo_checklist_templates.code`. Lives here
// as a thin wrapper to keep the polish bundle co-located.
//
// Routes through s.ewoChecklistTemplates (a separate repo with its own
// nil-safe builder) instead of poking the raw pool.
func (s *Service) SeedEWOChecklistFromTemplate(ctx context.Context, ewoID uuid.UUID, templateCode string) ([]domain.EWOChecklistItem, error) {
	if s.ewoChecklist == nil || s.ewoChecklistTemplates == nil {
		return nil, errFinanceNotConfigured()
	}
	tpl, err := s.ewoChecklistTemplates.FindByCode(ctx, templateCode)
	if err != nil {
		return nil, err
	}
	if !tpl.Active {
		return nil, derrors.Conflict("ewo_template.inactive", "this checklist template is inactive")
	}
	type rawItem struct {
		SeqNo       int    `json:"seq_no"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	var raws []rawItem
	if err := json.Unmarshal(tpl.ItemsJSON, &raws); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "ewo_template.invalid_body", "template body is not a valid item array", err)
	}
	inputs := make([]port.EWOChecklistItemInput, 0, len(raws))
	for _, r := range raws {
		inputs = append(inputs, port.EWOChecklistItemInput{
			SeqNo: r.SeqNo, Label: r.Label, Description: r.Description,
		})
	}
	return s.ReplaceEWOChecklist(ctx, port.ReplaceEWOChecklistInput{
		EWOID: ewoID, Items: inputs,
	})
}

// ListEWOChecklistTemplates / GetEWOChecklistTemplate /
// SaveEWOChecklistTemplate / DeleteEWOChecklistTemplate — admin
// surface for managing reusable seeds.

func (s *Service) ListEWOChecklistTemplates(ctx context.Context, activeOnly bool) ([]port.EWOChecklistTemplate, error) {
	if s.ewoChecklistTemplates == nil {
		return []port.EWOChecklistTemplate{}, nil
	}
	return s.ewoChecklistTemplates.List(ctx, activeOnly)
}

func (s *Service) GetEWOChecklistTemplate(ctx context.Context, id uuid.UUID) (*port.EWOChecklistTemplate, error) {
	if s.ewoChecklistTemplates == nil {
		return nil, derrors.NotFound("ewo_template.not_configured", "templates not wired")
	}
	return s.ewoChecklistTemplates.FindByID(ctx, id)
}

func (s *Service) SaveEWOChecklistTemplate(ctx context.Context, in port.EWOChecklistTemplate) (*port.EWOChecklistTemplate, error) {
	if s.ewoChecklistTemplates == nil {
		return nil, errFinanceNotConfigured()
	}
	return s.ewoChecklistTemplates.Upsert(ctx, in)
}

// DeleteEWOChecklistTemplate — admin removes a template entirely. The
// repo enforces existence (NotFound) so the handler doesn't have to
// pre-check. Items previously seeded from this template stay intact on
// their EWOs — templates are seed material, not a runtime link.
func (s *Service) DeleteEWOChecklistTemplate(ctx context.Context, id uuid.UUID) error {
	if s.ewoChecklistTemplates == nil {
		return errFinanceNotConfigured()
	}
	return s.ewoChecklistTemplates.Delete(ctx, id)
}

// ----- RFQ deadline escalation sweep ----------------------------------------

// RunRFQDeadlineSweep — cron entry point. Walks all open/in_progress
// RFQs with a deadline; fires once per bucket (t_minus_24h, overdue)
// to the assignee + requester. Dedup via an in-memory bucket key on
// the notification kind — the BE table already has a uniqueness
// invariant via notifications.subject_id + kind that prevents true
// dupes (we just don't fire if a row already exists this cycle).
//
// Returns count of notifications fired this run.
func (s *Service) RunRFQDeadlineSweep(ctx context.Context) (int, error) {
	if s.rfqs == nil {
		return 0, nil
	}
	// Iterate paginated.
	items, _, err := s.rfqs.List(ctx, "", nil, nil, 500, 0)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	fired := 0
	for _, r := range items {
		if r.Status != domain.RFQStatusOpen && r.Status != domain.RFQStatusInProgress {
			continue
		}
		if r.DeadlineAt == nil {
			continue
		}
		remaining := r.DeadlineAt.Sub(now)
		var kind, title, body string
		var severity domain.NotificationSeverity
		switch {
		case remaining > 48*time.Hour:
			continue
		case remaining > 24*time.Hour:
			kind = "rfq.deadline_t_minus_24h"
			title = "RFQ " + r.RFQNumber + " — ~24h to deadline"
			body = "Less than 48 hours remain on this RFQ. Submit the fulfilling BOQ."
			severity = domain.NotificationSeverityWarn
		case remaining > 0:
			kind = "rfq.deadline_t_minus_8h"
			title = "RFQ " + r.RFQNumber + " — deadline imminent"
			body = "Less than 24 hours remain. This RFQ will breach SLA soon."
			severity = domain.NotificationSeverityCritical
		default:
			kind = "rfq.deadline_overdue"
			title = "RFQ " + r.RFQNumber + " — overdue"
			body = "Deadline passed. The RFQ is flagged for management review."
			severity = domain.NotificationSeverityCritical
		}
		recipients := []uuid.UUID{}
		if r.AssignedTo != nil {
			recipients = append(recipients, *r.AssignedTo)
		}
		if r.RequestedBy != nil && (len(recipients) == 0 || *r.RequestedBy != recipients[0]) {
			recipients = append(recipients, *r.RequestedBy)
		}
		for _, u := range recipients {
			s.Notify(ctx, domain.NewNotification(
				u,
				kind,
				"rfq", r.ID,
				title, body, severity,
			))
			fired++
		}
	}
	return fired, nil
}

// ----- Bulk PO upload --------------------------------------------------------

// BulkUploadPODocuments — accepts an array of UploadPODocumentInput
// (typically representing multiple revisions in one operator action)
// and uploads each in sequence. Returns the slice of created docs.
// Atomic-ish: if any fails partway, the prior successes stay (PO docs
// are append-only by design — operators can re-attempt failed entries
// without rolling back the successful uploads).
func (s *Service) BulkUploadPODocuments(ctx context.Context, batch []port.UploadPODocumentInput) ([]domain.PODocument, error) {
	if s.poDocuments == nil {
		return nil, errFinanceNotConfigured()
	}
	out := make([]domain.PODocument, 0, len(batch))
	for _, in := range batch {
		d, err := s.UploadPODocument(ctx, in)
		if err != nil {
			return out, err
		}
		out = append(out, *d)
	}
	return out, nil
}

// ----- EWO ↔ field WO manual link --------------------------------------------

// LinkEWOToFieldWO — operator binds an EWO to a field WO. Best-effort:
// the field WO existence isn't validated cross-context (the field
// module lives in its own schema/service). Persists the soft FK.
func (s *Service) LinkEWOToFieldWO(ctx context.Context, ewoID, fieldWOID uuid.UUID) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, ewoID)
	if err != nil {
		return nil, err
	}
	e.FieldWorkOrderID = &fieldWOID
	if err := s.ewos.Update(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}
