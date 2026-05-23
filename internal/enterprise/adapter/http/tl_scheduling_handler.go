package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/internal/enterprise/usecase"
	"github.com/ion-core/backend/pkg/auth"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// TLSchedulingHandler is the Wave 96 HTTP surface for the team-lead
// scheduling workflow.
//
// Routes:
//
//   POST /api/enterprise/ewos/{id}/schedule          [enterprise.tl_scheduling.write]
//   POST /api/enterprise/ewos/{id}/reschedule        [enterprise.tl_scheduling.write]
//   POST /api/enterprise/ewos/{id}/start             [enterprise.tl_scheduling.write]
//   GET  /api/enterprise/ewos/scheduled              [enterprise.tl_scheduling.read]
//   GET  /api/enterprise/ewos/{id}/schedule-history  [enterprise.tl_scheduling.read]
//
// The /start route lives here (not on the FinanceHandler) because it's
// the TL-scheduling consumer of MarkEWOInProgress, which both flips
// status AND locks the schedule fields. The legacy /ewos/{id}/start
// route on FinanceHandler also still exists — Wave 96 keeps it live
// for backward compatibility but new clients should prefer the TL
// route which guarantees the schedule-lock semantics.
type TLSchedulingHandler struct {
	svc      *usecase.Service
	verifier *auth.Verifier
}

func NewTLSchedulingHandler(svc *usecase.Service, verifier *auth.Verifier) *TLSchedulingHandler {
	return &TLSchedulingHandler{svc: svc, verifier: verifier}
}

func (h *TLSchedulingHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Listing routes must come before the parameterized routes
		// otherwise chi treats "scheduled" as an id.
		r.With(httpserver.RequirePermission("enterprise.tl_scheduling.read")).
			Get("/ewos/scheduled", h.listScheduled)

		r.With(httpserver.RequirePermission("enterprise.tl_scheduling.write")).
			Post("/ewos/{id}/schedule", h.schedule)
		r.With(httpserver.RequirePermission("enterprise.tl_scheduling.write")).
			Post("/ewos/{id}/reschedule", h.reschedule)
		r.With(httpserver.RequirePermission("enterprise.tl_scheduling.write")).
			Post("/ewos/{id}/start", h.start)

		r.With(httpserver.RequirePermission("enterprise.tl_scheduling.read")).
			Get("/ewos/{id}/schedule-history", h.scheduleHistory)
	})
}

// =====================================================================
// Handlers
// =====================================================================

type scheduleEWORequest struct {
	StartDate    string  `json:"start_date"`    // RFC3339 or YYYY-MM-DD
	EndDate      string  `json:"end_date"`      // RFC3339 or YYYY-MM-DD
	TeamLeadID   string  `json:"team_lead_id"`  // required
	TechnicianID *string `json:"technician_id,omitempty"`
}

func (h *TLSchedulingHandler) schedule(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req scheduleEWORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	start, err := parseScheduleTime(req.StartDate, "start_date")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	end, err := parseScheduleTime(req.EndDate, "end_date")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	teamLead, err := parseUUIDLocal(req.TeamLeadID, "team_lead_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var technician *uuid.UUID
	if req.TechnicianID != nil && *req.TechnicianID != "" {
		t, err := parseUUIDLocal(*req.TechnicianID, "technician_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		technician = &t
	}
	e, err := h.svc.ScheduleEWO(r.Context(), id, start, end, teamLead, technician)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toExtendedEWODTO(*e))
}

type rescheduleEWORequest struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Reason    string `json:"reason"`
}

func (h *TLSchedulingHandler) reschedule(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req rescheduleEWORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	start, err := parseScheduleTime(req.StartDate, "start_date")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	end, err := parseScheduleTime(req.EndDate, "end_date")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	by := actorUserIDLocal(r.Context())
	if by == nil {
		httpserver.WriteError(w, derrors.Validation(
			"ewo.reschedule_by_required",
			"reschedule requires an authenticated actor",
		))
		return
	}
	e, err := h.svc.RescheduleEWO(r.Context(), id, start, end, *by, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toExtendedEWODTO(*e))
}

func (h *TLSchedulingHandler) start(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	e, err := h.svc.MarkEWOInProgress(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toExtendedEWODTO(*e))
}

func (h *TLSchedulingHandler) listScheduled(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.EWOListFilter{
		Status: q.Get("status"),
		Limit:  parseIntDefaultLocal(q.Get("page_size"), 100),
		Offset: 0,
	}
	if s := q.Get("team_lead_id"); s != "" {
		u, err := parseUUIDLocal(s, "team_lead_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.AssignedTeamLeadUserID = &u
	}
	if s := q.Get("technician_id"); s != "" {
		u, err := parseUUIDLocal(s, "technician_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.AssignedTechnicianUserID = &u
	}
	if s := q.Get("side"); s != "" {
		f.Side = s
	}
	if s := q.Get("scheduled_from"); s != "" {
		t, err := parseScheduleTime(s, "scheduled_from")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.ScheduledFrom = &t
	}
	if s := q.Get("scheduled_to"); s != "" {
		t, err := parseScheduleTime(s, "scheduled_to")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.ScheduledTo = &t
	}
	items, total, err := h.svc.ListScheduledEWOs(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]extendedEWODTO, 0, len(items))
	for _, e := range items {
		out = append(out, toExtendedEWODTO(e))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": total,
	})
}

func (h *TLSchedulingHandler) scheduleHistory(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	entries, err := h.svc.ListEWOScheduleHistory(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]scheduleHistoryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toScheduleHistoryDTO(e))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
	})
}

// =====================================================================
// DTOs
// =====================================================================

// extendedEWODTO is the Wave 96 EWO response shape — superset of the
// pre-Wave-96 ewoDTO (which lives in finance_handler.go). The base
// fields are duplicated so the wire format is stable for clients that
// fetch from either endpoint.
type extendedEWODTO struct {
	ID            string  `json:"id"`
	EWONumber     string  `json:"ewo_number"`
	QuotationID   string  `json:"quotation_id"`
	OpportunityID string  `json:"opportunity_id"`
	BOQVersionID  string  `json:"boq_version_id"`
	Status        string  `json:"status"`
	AssignedTo    *string `json:"assigned_to,omitempty"`
	StartedAt     *string `json:"started_at,omitempty"`
	CompletedAt   *string `json:"completed_at,omitempty"`
	CancelledAt   *string `json:"cancelled_at,omitempty"`
	CancelReason  string  `json:"cancel_reason"`
	Notes         string  `json:"notes"`
	Revision      int     `json:"revision"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`

	// Wave 96 — dual + scheduling fields.
	Side                     string  `json:"side"`
	ExecutingSubsidiaryID    *string `json:"executing_subsidiary_id,omitempty"`
	IntercompanyPOID         *string `json:"intercompany_po_id,omitempty"`
	PairedEWOID              *string `json:"paired_ewo_id,omitempty"`
	ScheduledStart           *string `json:"scheduled_start_date,omitempty"`
	ScheduledEnd             *string `json:"scheduled_end_date,omitempty"`
	DurationDays             *int    `json:"duration_days,omitempty"`
	AssignedTechnicianUserID *string `json:"assigned_technician_user_id,omitempty"`
	AssignedTeamLeadUserID   *string `json:"assigned_team_lead_user_id,omitempty"`
	ScheduleLocked           bool    `json:"schedule_locked"`
}

func toExtendedEWODTO(e domain.EWO) extendedEWODTO {
	d := extendedEWODTO{
		ID:             e.ID.String(),
		EWONumber:      e.EWONumber,
		QuotationID:    e.QuotationID.String(),
		OpportunityID:  e.OpportunityID.String(),
		BOQVersionID:   e.BOQVersionID.String(),
		Status:         string(e.Status),
		CancelReason:   e.CancelReason,
		Notes:          e.Notes,
		Revision:       e.Revision,
		CreatedAt:      rfc3339(e.CreatedAt),
		UpdatedAt:      rfc3339(e.UpdatedAt),
		Side:           string(e.Side),
		ScheduleLocked: e.ScheduleLocked,
		DurationDays:   e.DurationDays,
	}
	if e.AssignedTo != nil {
		s := e.AssignedTo.String()
		d.AssignedTo = &s
	}
	if e.StartedAt != nil {
		s := rfc3339(*e.StartedAt)
		d.StartedAt = &s
	}
	if e.CompletedAt != nil {
		s := rfc3339(*e.CompletedAt)
		d.CompletedAt = &s
	}
	if e.CancelledAt != nil {
		s := rfc3339(*e.CancelledAt)
		d.CancelledAt = &s
	}
	if e.ExecutingSubsidiaryID != nil {
		s := e.ExecutingSubsidiaryID.String()
		d.ExecutingSubsidiaryID = &s
	}
	if e.IntercompanyPOID != nil {
		s := e.IntercompanyPOID.String()
		d.IntercompanyPOID = &s
	}
	if e.PairedEWOID != nil {
		s := e.PairedEWOID.String()
		d.PairedEWOID = &s
	}
	if e.ScheduledStartDate != nil {
		s := rfc3339(*e.ScheduledStartDate)
		d.ScheduledStart = &s
	}
	if e.ScheduledEndDate != nil {
		s := rfc3339(*e.ScheduledEndDate)
		d.ScheduledEnd = &s
	}
	if e.AssignedTechnicianUserID != nil {
		s := e.AssignedTechnicianUserID.String()
		d.AssignedTechnicianUserID = &s
	}
	if e.AssignedTeamLeadUserID != nil {
		s := e.AssignedTeamLeadUserID.String()
		d.AssignedTeamLeadUserID = &s
	}
	return d
}

type scheduleHistoryDTO struct {
	ID             string  `json:"id"`
	EWOID          string  `json:"ewo_id"`
	PrevStart      string  `json:"prev_start"`
	PrevEnd        string  `json:"prev_end"`
	PrevTeamLead   *string `json:"prev_team_lead,omitempty"`
	PrevTechnician *string `json:"prev_technician,omitempty"`
	ChangedBy      string  `json:"changed_by"`
	ChangedAt      string  `json:"changed_at"`
	Reason         string  `json:"reason"`
}

func toScheduleHistoryDTO(e domain.ScheduleHistoryEntry) scheduleHistoryDTO {
	d := scheduleHistoryDTO{
		ID:        e.ID.String(),
		EWOID:     e.EWOID.String(),
		PrevStart: rfc3339(e.PrevStart),
		PrevEnd:   rfc3339(e.PrevEnd),
		ChangedBy: e.ChangedBy.String(),
		ChangedAt: rfc3339(e.ChangedAt),
		Reason:    e.Reason,
	}
	if e.PrevTeamLead != uuid.Nil {
		s := e.PrevTeamLead.String()
		d.PrevTeamLead = &s
	}
	if e.PrevTechnician != uuid.Nil {
		s := e.PrevTechnician.String()
		d.PrevTechnician = &s
	}
	return d
}

// =====================================================================
// Helpers
// =====================================================================

// parseScheduleTime accepts RFC3339, RFC3339 without timezone, or
// plain YYYY-MM-DD (treated as midnight UTC).
func parseScheduleTime(s, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, derrors.Validation(
			"ewo.schedule."+field+"_required",
			field+" is required",
		)
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, derrors.Validation(
		"ewo.schedule."+field+"_invalid",
		field+" must be RFC 3339 or YYYY-MM-DD",
	)
}
