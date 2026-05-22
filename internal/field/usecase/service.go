// Package usecase implements the field bounded context's business rules.
//
// Core flows:
//
//	CreateWOFromOrder: look up order+customer via CRM gateway → build WO,
//	                   stamp branch_id from customer, status=unassigned.
//	RouteToTeam:       attach a team + team_leader; status=assigned.
//	AssignTechnicians: write the (lead, observer?) pair via UpsertPair.
//	UpdateStatus:      state-machine check via domain.AssertCanTransition.
//	SubmitChecklist:   upsert one (wo_id, template_item_id) response.
//	AddResolutionItem: append to the on-site log; auto-increment item_order.
//	SubmitBAST:        validate that the active BAST doesn't exist, build a
//	                   compiled snapshot (or accept the caller's), insert,
//	                   bump WO to pending_noc_verification.
//	VerifyBAST:        NOC decision → approved/rejected. On approve, bump WO
//	                   to completed. On reject, drop WO back to in_progress
//	                   so the tech can fix and resubmit (new BAST row).
package usecase

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Service struct {
	wos        port.WORepository
	assigns    port.AssignmentRepository
	checklists port.ChecklistRepository
	resolutions port.ResolutionRepository
	basts      port.BASTRepository
	teams      port.TeamRepository
	crm        port.CRMGateway
	billing    port.BillingGateway       // optional; nil = no payment gate
	reschedules port.RescheduleRepository // M5 r2 — optional
	uploads    port.UploadsGateway        // M5 r3 — optional; nil disables GPS gate
	radius     port.RadiusReader          // optional; nil disables ONT config endpoint
	activation port.ActivationGateway     // optional; nil = no auto-activation on BAST approve
}

// WithActivation attaches the activation gateway so VerifyBAST(approved)
// for an install WO automatically provisions + promotes the RADIUS
// account and flips the customer to 'active'. Optional — without it,
// the BAST verify still works but operators must activate manually.
func (s *Service) WithActivation(a port.ActivationGateway) *Service {
	s.activation = a
	return s
}

// WithRadius attaches the radius reader so techs can pull ONT
// credentials during an active WO. Optional — older callers stay nil-safe.
func (s *Service) WithRadius(r port.RadiusReader) *Service {
	s.radius = r
	return s
}

// WithUploads attaches the uploads gateway so checklist responses with
// gps_required=true verify the photo's GPS metadata before accepting.
// Optional (earlier callers stay nil-safe).
func (s *Service) WithUploads(u port.UploadsGateway) *Service {
	s.uploads = u
	return s
}

func NewService(
	wos port.WORepository,
	assigns port.AssignmentRepository,
	checklists port.ChecklistRepository,
	resolutions port.ResolutionRepository,
	basts port.BASTRepository,
	teams port.TeamRepository,
	crm port.CRMGateway,
) *Service {
	return &Service{
		wos: wos, assigns: assigns, checklists: checklists,
		resolutions: resolutions, basts: basts, teams: teams, crm: crm,
	}
}

// WithBilling attaches the billing gateway so VerifyBAST enforces the
// payment-gates-NOC rule. Optional — M5 round-1 wiring left this nil
// and the M6 wiring (field-svc embedding billing usecase) sets it.
func (s *Service) WithBilling(b port.BillingGateway) *Service {
	s.billing = b
	return s
}

var _ port.UseCase = (*Service)(nil)

// =====================================================================
// WO lifecycle
// =====================================================================

// CreateTerminationWO mints a termination work-order. The billing
// service calls this from the voluntary + auto-termination flows. We
// don't go through the CRM gateway because the caller already supplies
// the customer + address (it was looked up to mint the termination
// request row).
func (s *Service) CreateTerminationWO(ctx context.Context, in port.CreateTerminationWOInput) (*port.WODetail, error) {
	w, err := domain.NewTerminationWO(in.OrderID, in.CustomerID, in.Address)
	if err != nil {
		return nil, err
	}
	w.BranchID = in.BranchID
	if in.CreatedBy != uuid.Nil {
		cb := in.CreatedBy
		w.CreatedBy = &cb
	}
	w.Notes = in.Notes
	w.Status = domain.WOStatusUnassigned
	if err := s.wos.Create(ctx, w); err != nil {
		return nil, err
	}
	return s.GetWO(ctx, w.ID)
}

func (s *Service) CreateWOFromOrder(ctx context.Context, in port.CreateWOFromOrderInput) (*port.WODetail, error) {
	proj, err := s.crm.OrderForWO(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	w, err := domain.NewInstallationWO(&proj.OrderID, proj.CustomerID, proj.Address)
	if err != nil {
		return nil, err
	}
	w.BranchID = proj.BranchID
	if in.Priority != "" {
		w.Priority = in.Priority
	}
	w.Notes = in.Notes
	if in.CreatedBy != uuid.Nil {
		cb := in.CreatedBy
		w.CreatedBy = &cb
	}
	if in.ScheduledDate != nil {
		w.ScheduledDate = in.ScheduledDate
		// M5 r2 — set SLA due to scheduled_date + 24h as a hardcoded
		// default. Round-3 will read the product's SLA window from
		// platform_config / product.temp_activation_window_hours.
		due := in.ScheduledDate.Add(24 * time.Hour)
		w.SLADueAt = &due
	}
	// New WOs go straight to 'unassigned' so the Team Leader's queue can
	// pick them up. The 'created' status is reserved for soft-creates that
	// might not have an order yet (not used in round 1).
	w.Status = domain.WOStatusUnassigned

	if err := s.wos.Create(ctx, w); err != nil {
		return nil, err
	}
	return s.GetWO(ctx, w.ID)
}

func (s *Service) ListWOs(ctx context.Context, f port.WOListFilter) ([]port.WODetail, int, error) {
	return s.wos.List(ctx, f)
}

// GetWO composes a full detail view by pulling header + assignments +
// checklist (template + responses) + resolution items + active BAST.
// All five lookups run sequentially against the same pool; round 1
// does not parallelise — at the scale we're at, one query at a time
// is well under the per-request budget.
func (s *Service) GetWO(ctx context.Context, id uuid.UUID) (*port.WODetail, error) {
	d, err := s.wos.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	assigns, err := s.assigns.ListForWO(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Assignments = assigns

	tpl, items, err := s.checklists.FindTemplateFor(ctx, d.WO.WOType, d.WO.ProductType, d.WO.MaintenanceSubtype)
	if err == nil { // template absence is acceptable for unusual types
		d.ChecklistTemplate = tpl
		d.ChecklistItems = items
	}
	responses, err := s.checklists.ListResponses(ctx, id)
	if err != nil {
		return nil, err
	}
	d.ChecklistResponses = responses

	res, err := s.resolutions.ListForWO(ctx, id)
	if err != nil {
		return nil, err
	}
	d.ResolutionItems = res

	bast, err := s.basts.FindActiveForWO(ctx, id)
	if err != nil {
		return nil, err
	}
	d.ActiveBAST = bast

	return d, nil
}

func (s *Service) RouteToTeam(ctx context.Context, in port.RouteToTeamInput) (*port.WODetail, error) {
	d, err := s.wos.FindByID(ctx, in.WOID)
	if err != nil {
		return nil, err
	}
	team, err := s.teams.FindByID(ctx, in.TeamID)
	if err != nil {
		return nil, err
	}
	w := &d.WO
	w.TeamID = &team.Team.ID
	w.TeamLeaderID = team.Team.TeamLeaderID
	// Branch cross-cut: detect cross-area dispatch so reports can show it.
	if w.BranchID != nil && *w.BranchID != team.Team.BranchID {
		w.IsCrossArea = true
	}
	if err := s.wos.Update(ctx, w); err != nil {
		return nil, err
	}
	return s.GetWO(ctx, in.WOID)
}

func (s *Service) AssignTechnicians(ctx context.Context, in port.AssignTechniciansInput) (*port.WODetail, error) {
	d, err := s.wos.FindByID(ctx, in.WOID)
	if err != nil {
		return nil, err
	}
	if in.LeadID == uuid.Nil {
		return nil, derrors.Validation("assign.lead_required", "lead technician is required")
	}
	if in.ObserverID != nil && *in.ObserverID == in.LeadID {
		return nil, derrors.Validation("assign.duplicate", "lead and observer must differ")
	}

	now := time.Now().UTC()
	lead := domain.Assignment{
		ID:           uuid.New(),
		WOID:         in.WOID,
		TechnicianID: in.LeadID,
		Grade:        in.LeadGrade,
		WORole:       domain.WORoleLead,
		AssignedBy:   &in.AssignedBy,
		AssignedAt:   now,
	}
	if lead.Grade == "" {
		lead.Grade = domain.GradeSenior
	}
	var observer *domain.Assignment
	if in.ObserverID != nil {
		grade := domain.GradeJunior
		if in.ObserverGrade != nil {
			grade = *in.ObserverGrade
		}
		observer = &domain.Assignment{
			ID:           uuid.New(),
			WOID:         in.WOID,
			TechnicianID: *in.ObserverID,
			Grade:        grade,
			WORole:       domain.WORoleObserver,
			AssignedBy:   &in.AssignedBy,
			AssignedAt:   now,
		}
	}
	if err := s.assigns.UpsertPair(ctx, in.WOID, lead, observer); err != nil {
		return nil, err
	}

	// Bump status to 'assigned' if the WO was sitting in unassigned/rescheduled.
	if d.WO.Status == domain.WOStatusUnassigned || d.WO.Status == domain.WOStatusRescheduled {
		if err := s.wos.UpdateStatus(ctx, in.WOID, domain.WOStatusAssigned); err != nil {
			return nil, err
		}
	}
	return s.GetWO(ctx, in.WOID)
}

func (s *Service) UpdateStatus(ctx context.Context, in port.UpdateWOStatusInput) (*port.WODetail, error) {
	d, err := s.wos.FindByID(ctx, in.WOID)
	if err != nil {
		return nil, err
	}
	if err := d.WO.AssertCanTransition(in.Status); err != nil {
		return nil, err
	}
	if err := s.wos.UpdateStatus(ctx, in.WOID, in.Status); err != nil {
		return nil, err
	}
	return s.GetWO(ctx, in.WOID)
}

// =====================================================================
// Checklist + resolution
// =====================================================================

func (s *Service) SubmitChecklistResponse(ctx context.Context, in port.SubmitChecklistResponseInput) (*domain.ChecklistResponse, error) {
	if in.WOID == uuid.Nil || in.TemplateItemID == uuid.Nil {
		return nil, derrors.Validation("checklist.ids_required", "wo_id and template_item_id are required")
	}

	// M5 r3 — GPS gate: when the template item requires GPS and the
	// response references an uploaded photo, the upload's metadata
	// must include GPS coords (from EXIF or client headers). For
	// non-photo gps_required items the inline gps_lat/gps_lng in
	// the request body is enough.
	gpsLat := in.GPSLat
	gpsLng := in.GPSLng
	item, err := s.checklists.FindItem(ctx, in.TemplateItemID)
	if err == nil && item != nil && item.GPSRequired {
		if in.FileURL != "" && s.uploads != nil {
			lat, lng, gerr := s.uploads.GPSFor(ctx, in.FileURL)
			if gerr != nil {
				return nil, derrors.Validation("checklist.upload_unknown",
					"file_url is not a known upload — re-upload the photo")
			}
			if lat == nil || lng == nil {
				return nil, derrors.Validation("checklist.gps_missing",
					"this item requires GPS; the uploaded photo has no GPS metadata. "+
						"Enable location on the camera or attach client GPS at upload time.")
			}
			// Persist the upload's GPS onto the response so the audit
			// trail shows where the photo was taken.
			gpsLat, gpsLng = lat, lng
		} else if gpsLat == nil || gpsLng == nil {
			return nil, derrors.Validation("checklist.gps_required",
				"this item requires gps_lat + gps_lng on the response")
		}
	}

	r := &domain.ChecklistResponse{
		ID:             uuid.New(),
		WOID:           in.WOID,
		TemplateItemID: in.TemplateItemID,
		ResponseText:   in.ResponseText,
		FileURL:        in.FileURL,
		GPSLat:         gpsLat,
		GPSLng:         gpsLng,
		GPSAccuracyM:   in.GPSAccuracyM,
		SubmittedBy:    &in.SubmittedBy,
		SubmittedAt:    time.Now().UTC(),
	}
	return s.checklists.UpsertResponse(ctx, r)
}

func (s *Service) AddResolutionItem(ctx context.Context, in port.AddResolutionItemInput) (*domain.ResolutionItem, error) {
	existing, err := s.resolutions.ListForWO(ctx, in.WOID)
	if err != nil {
		return nil, err
	}
	next := 1
	for _, e := range existing {
		if e.ItemOrder >= next {
			next = e.ItemOrder + 1
		}
	}
	ri := &domain.ResolutionItem{
		ID:               uuid.New(),
		WOID:             in.WOID,
		ItemOrder:        next,
		ItemLabel:        in.ItemLabel,
		Category:         in.Category,
		Finding:          in.Finding,
		ActionTaken:      in.ActionTaken,
		ResolutionStatus: in.ResolutionStatus,
		TimeSpentMinutes: in.TimeSpentMinutes,
		ResolvedBy:       &in.ResolvedBy,
		LoggedAt:         time.Now().UTC(),
	}
	if err := s.resolutions.Add(ctx, ri); err != nil {
		return nil, err
	}
	return ri, nil
}

// =====================================================================
// BAST
// =====================================================================

// SubmitBAST creates the immutable signoff record and pushes the WO into
// pending_noc_verification. The compiled_data payload is the snapshot of
// what's on screen: customer, checklist (responses + template labels),
// resolution items, technician.
//
// Round-1 *DOES* check that all required checklist items have responses.
// It does NOT yet enforce the "OTC invoice paid" gate — that lives in M6.
func (s *Service) SubmitBAST(ctx context.Context, in port.SubmitBASTInput) (*domain.BAST, error) {
	d, err := s.wos.FindByID(ctx, in.WOID)
	if err != nil {
		return nil, err
	}
	if d.WO.Status != domain.WOStatusInProgress {
		return nil, derrors.Conflict("bast.wo_not_in_progress",
			"WO must be in_progress to submit BAST")
	}

	// Refuse double-submit: an active (non-rejected) BAST already exists.
	if existing, _ := s.basts.FindActiveForWO(ctx, in.WOID); existing != nil {
		return nil, derrors.Conflict("bast.already_submitted",
			"a BAST is already submitted for this WO (status: "+string(existing.NOCStatus)+")")
	}

	// Required-checklist gate.
	tpl, items, _ := s.checklists.FindTemplateFor(ctx, d.WO.WOType, d.WO.ProductType, d.WO.MaintenanceSubtype)
	if tpl != nil {
		responses, err := s.checklists.ListResponses(ctx, in.WOID)
		if err != nil {
			return nil, err
		}
		responded := map[uuid.UUID]bool{}
		for _, r := range responses {
			responded[r.TemplateItemID] = true
		}
		for _, it := range items {
			if it.Required && !responded[it.ID] {
				return nil, derrors.Validation("bast.checklist_incomplete",
					"required checklist item not submitted: "+it.Label)
			}
		}
	}

	compiled := in.CompiledData
	if len(compiled) == 0 {
		// Build a minimal snapshot if caller didn't supply one. Round 1:
		// just include checklist + resolution + a header dict.
		responses, _ := s.checklists.ListResponses(ctx, in.WOID)
		resItems, _ := s.resolutions.ListForWO(ctx, in.WOID)
		snap := map[string]any{
			"wo_number":     d.WO.WONumber,
			"customer_id":   d.WO.CustomerID.String(),
			"address":       d.WO.Address,
			"product_type":  d.WO.ProductType,
			"signoff_mode":  string(in.SignOffMode),
			"checklist":     responses,
			"resolution":    resItems,
		}
		if b, err := json.Marshal(snap); err == nil {
			compiled = b
		}
	}

	b, err := domain.NewBAST(in.WOID, d.WO.CustomerID, in.SignOffMode, compiled)
	if err != nil {
		return nil, err
	}
	b.CustomerSigURL = in.CustomerSigURL
	b.OTPUsed = in.OTPUsed
	b.SignOffGPSLat = in.GPSLat
	b.SignOffGPSLng = in.GPSLng
	by := in.SubmittedBy
	b.SubmittedBy = &by

	// M5 r2 — remote sign-off generates a one-shot OTP. We persist only
	// the bcrypt hash; the plaintext goes back to the caller via the
	// in-memory OTPPlaintextOnce field so the HTTP layer can return it
	// exactly once (then the field clears). Out-of-band delivery
	// (WhatsApp/SMS) lands in M6 r2 / round-3.
	if in.SignOffMode == domain.SignOffRemote {
		plain, hashed, gerr := generate6DigitOTP()
		if gerr != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "bast.otp_gen",
				"failed to generate otp", gerr)
		}
		b.OTPUsed = true
		b.OTPCode = hashed
		b.OTPPlaintextOnce = plain
	}

	if err := s.basts.Create(ctx, b); err != nil {
		return nil, err
	}

	if err := s.wos.UpdateStatus(ctx, in.WOID, domain.WOStatusPendingNOCVerification); err != nil {
		return nil, err
	}
	return b, nil
}

// GetONTConfig returns the RADIUS credential projection used by the
// Tech App's on-site ONT setup screen. The endpoint is intentionally
// gated to active WOs only — once the WO completes, the credentials
// stop being visible from this surface and ops use the back-office
// network view instead.
func (s *Service) GetONTConfig(ctx context.Context, woID uuid.UUID, callerID uuid.UUID) (*port.RadiusAccountView, error) {
	if s.radius == nil {
		return nil, derrors.New(derrors.KindInternal, "field.no_radius",
			"radius reader not configured")
	}
	d, err := s.wos.FindByID(ctx, woID)
	if err != nil {
		return nil, err
	}
	if d.WO.Status != domain.WOStatusInProgress {
		return nil, derrors.Forbidden("wo.ont_inactive",
			"ONT config is only available while the WO is in progress")
	}
	// Per-assignment scoping (PRD §6.2.1 line 3133): the password card
	// is only visible to the technicians actually working the WO. We
	// look the assignments up here rather than relying on the caller
	// because permissions alone (field.wo.read) are granted to many
	// roles. A nil caller bypasses the check — admin tooling only.
	if callerID != uuid.Nil {
		assigns, err := s.assigns.ListForWO(ctx, woID)
		if err != nil {
			return nil, err
		}
		assigned := false
		for _, a := range assigns {
			if a.Assignment.TechnicianID == callerID {
				assigned = true
				break
			}
		}
		if !assigned {
			return nil, derrors.Forbidden("wo.ont_not_assigned",
				"only the assigned technicians may view ONT credentials")
		}
	}
	return s.radius.RadiusAccountFor(ctx, d.WO.CustomerID)
}

// VerifyBAST is the NOC decision. Approve drives WO → completed.
// Reject drives WO → in_progress so the tech can fix + resubmit; a new
// BAST row is created on resubmit (the rejected one stays as history).
func (s *Service) VerifyBAST(ctx context.Context, in port.VerifyBASTInput) (*domain.BAST, error) {
	b, err := s.basts.FindByID(ctx, in.BASTID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, derrors.NotFound("bast.not_found", "bast not found")
	}
	if err := b.CanVerify(); err != nil {
		return nil, err
	}
	if in.Decision != domain.NOCStatusApproved && in.Decision != domain.NOCStatusRejected {
		return nil, derrors.Validation("bast.decision_invalid",
			"decision must be approved or rejected")
	}

	// Payment-gates-NOC: the single load-bearing sequencing rule of M6.
	// We only block APPROVAL, not rejection — NOC can always reject.
	if in.Decision == domain.NOCStatusApproved && s.billing != nil {
		wo, err := s.wos.FindByID(ctx, b.WOID)
		if err != nil {
			return nil, err
		}
		if wo.WO.OrderID != nil {
			paid, err := s.billing.IsOrderOTCPaid(ctx, *wo.WO.OrderID)
			if err != nil {
				return nil, err
			}
			if !paid {
				return nil, derrors.Conflict("bast.payment_gate",
					"OTC invoice for this order is not yet paid; NOC approval blocked")
			}
		}
	}

	if err := s.basts.MarkVerified(ctx, b.ID, in.Decision, in.VerifiedBy, in.Notes); err != nil {
		return nil, err
	}

	// Drive WO status.
	target := domain.WOStatusCompleted
	if in.Decision == domain.NOCStatusRejected {
		target = domain.WOStatusInProgress
	}
	if err := s.wos.UpdateStatus(ctx, b.WOID, target); err != nil {
		return nil, err
	}

	// Approval-side cross-cuts.
	//
	// Two hooks fire here, both gated on Approval and on knowing the
	// WO's type:
	//
	//   - new_installation: provision the RADIUS account (TEMPORARY),
	//     promote it to PERMANENT_ACTIVE, and flip the customer to
	//     'active'. This is the moment a paying customer's account
	//     actually comes online.
	//
	//   - termination: signal billing to flip the termination_request
	//     to completed, mark the customer as 'terminated', and drive
	//     RADIUS DEACTIVATED.
	//
	// We re-look up the WO once so both hooks share the type read.
	if in.Decision == domain.NOCStatusApproved {
		woDetail, err := s.wos.FindByID(ctx, b.WOID)
		if err == nil {
			switch woDetail.WO.WOType {
			case domain.WOTypeNewInstallation:
				if err := s.onInstallApproved(ctx, &woDetail.WO); err != nil {
					return nil, derrors.Wrap(derrors.KindInternal,
						"bast.activation_hook",
						"install activation hook failed", err)
				}
			case domain.WOTypeTermination:
				if s.billing != nil {
					if err := s.billing.OnTerminationWOCompleted(ctx, b.WOID); err != nil {
						return nil, derrors.Wrap(derrors.KindInternal,
							"bast.termination_hook",
							"termination complete hook failed", err)
					}
				}
			}
		}
	}
	return s.basts.FindByID(ctx, b.ID)
}

// onInstallApproved provisions RADIUS + promotes to PERMANENT_ACTIVE +
// flips the customer status. Either dependency may be nil-wired — the
// service stays usable; the hook just becomes a no-op.
//
// We intentionally don't roll back: if RADIUS provisioning fails, the
// customer flip doesn't run; if the flip fails, the RADIUS state stays
// PERMANENT. Both are forward-progress states and the next operator
// retry (or a manual nudge) closes the loop.
func (s *Service) onInstallApproved(ctx context.Context, wo *domain.WorkOrder) error {
	if wo.OrderID == nil {
		// Defensive: install WOs always have an order, but if one slips
		// through we don't have enough info to provision RADIUS. Skip
		// the hook rather than fail the BAST verify.
		return nil
	}
	if s.activation != nil && s.crm != nil {
		proj, err := s.crm.ActivationProjectionForOrder(ctx, *wo.OrderID)
		if err != nil {
			return err
		}
		if err := s.activation.ProvisionAndActivate(ctx, *proj); err != nil {
			return err
		}
	}
	if s.crm != nil {
		if err := s.crm.SetCustomerActive(ctx, wo.CustomerID); err != nil {
			return err
		}
	}
	return nil
}

// =====================================================================
// Teams
// =====================================================================

func (s *Service) CreateTeam(ctx context.Context, in port.CreateTeamInput) (*port.TeamView, error) {
	t, err := domain.NewTeam(in.Code, in.Name, in.BranchID, in.TeamLeaderID)
	if err != nil {
		return nil, err
	}
	if err := s.teams.Create(ctx, t); err != nil {
		return nil, err
	}
	return s.teams.FindByID(ctx, t.ID)
}

func (s *Service) ListTeams(ctx context.Context, branchID *uuid.UUID, activeOnly bool) ([]port.TeamView, error) {
	return s.teams.List(ctx, branchID, activeOnly)
}

func (s *Service) GetTeam(ctx context.Context, id uuid.UUID) (*port.TeamView, error) {
	return s.teams.FindByID(ctx, id)
}

func (s *Service) AddTeamMember(ctx context.Context, in port.AddTeamMemberInput) (*port.TeamMemberView, error) {
	if _, err := s.teams.FindByID(ctx, in.TeamID); err != nil {
		return nil, err
	}
	m := &domain.TeamMember{
		ID:       uuid.New(),
		TeamID:   in.TeamID,
		UserID:   in.UserID,
		Grade:    in.Grade,
		Active:   true,
		JoinedAt: time.Now().UTC(),
	}
	if err := s.teams.AddMember(ctx, m); err != nil {
		return nil, err
	}
	members, err := s.teams.ListMembers(ctx, in.TeamID)
	if err != nil {
		return nil, err
	}
	for _, mv := range members {
		if mv.Member.ID == m.ID {
			return &mv, nil
		}
	}
	return &port.TeamMemberView{Member: *m}, nil
}

func (s *Service) ListTeamMembers(ctx context.Context, teamID uuid.UUID) ([]port.TeamMemberView, error) {
	return s.teams.ListMembers(ctx, teamID)
}
