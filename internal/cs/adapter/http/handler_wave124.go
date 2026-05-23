// Wave 124 — HTTP handlers for SLA + Service Requests + Teams + WO-from-Ticket
// + CSAT + Communications.
//
// All handlers expect their corresponding usecase wired on the Handler
// struct; Mount() only attaches the routes when the usecase is non-nil
// so partial deployments don't 500.
package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// SLA Matrix + per-ticket SLA
// =====================================================================

func (h *Handler) listSLAMatrix(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.SLAMatrixFilter{
		CustomerType: q.Get("customer_type"),
		TicketType:   q.Get("ticket_type"),
		Priority:     q.Get("priority"),
		OnlyActive:   q.Get("only_active") != "false",
		Limit:        httpserver.ParseIntDefault(q.Get("limit"), 500),
		Offset:       httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	rows, err := h.sla.GetMatrix(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]slaMatrixDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, toSLAMatrixDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type upsertSLAMatrixRequest struct {
	CustomerType         string  `json:"customer_type"`
	TicketType           string  `json:"ticket_type"`
	Priority             string  `json:"priority"`
	FirstResponseMinutes int     `json:"first_response_minutes"`
	ResolveMinutes       int     `json:"resolve_minutes"`
	BreachWarnPct        float64 `json:"breach_warn_pct,omitempty"`
	EffectiveFrom        string  `json:"effective_from"`
	IsActive             *bool   `json:"is_active,omitempty"`
}

func (h *Handler) upsertSLAMatrix(w http.ResponseWriter, r *http.Request) {
	var req upsertSLAMatrixRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	from := time.Now().UTC()
	if req.EffectiveFrom != "" {
		if t, err := time.Parse("2006-01-02", req.EffectiveFrom); err == nil {
			from = t.UTC()
		}
	}
	pct := req.BreachWarnPct
	if pct <= 0 {
		pct = 0.80
	}
	e, err := domain.NewSLAMatrixEntry(
		domain.CustomerType(req.CustomerType),
		domain.TicketType(req.TicketType),
		domain.Priority(req.Priority),
		req.FirstResponseMinutes,
		req.ResolveMinutes,
		pct,
		from,
	)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.IsActive != nil {
		e.IsActive = *req.IsActive
	}
	if err := h.sla.UpsertMatrix(r.Context(), e); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSLAMatrixDTO(*e))
}

func (h *Handler) getTicketSLA(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	summary, err := h.sla.TicketSLA(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketSLADTO(summary))
}

// =====================================================================
// Service Requests
// =====================================================================

type submitSRRequest struct {
	CustomerID  string         `json:"customer_id"`
	TicketID    string         `json:"ticket_id,omitempty"`
	RequestType string         `json:"request_type"`
	OpenedVia   string         `json:"opened_via,omitempty"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Priority    string         `json:"priority,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
}

func (h *Handler) submitServiceRequest(w http.ResponseWriter, r *http.Request) {
	var req submitSRRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	customerID, err := parseUUID(req.CustomerID, "customer_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	in := port.SubmitServiceRequestInput{
		CustomerID:  customerID,
		RequestType: domain.ServiceRequestType(req.RequestType),
		SubmittedBy: actorUserID(r.Context()),
		OpenedVia:   domain.OpenedVia(req.OpenedVia),
		Title:       req.Title,
		Description: req.Description,
		Priority:    domain.Priority(req.Priority),
		Payload:     req.Payload,
	}
	if req.TicketID != "" {
		tid, err := parseUUID(req.TicketID, "ticket_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.TicketID = &tid
	}
	sr, err := h.srs.Submit(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSRDTO(*sr))
}

func (h *Handler) listServiceRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.ServiceRequestFilter{
		Status:      q.Get("status"),
		RequestType: q.Get("request_type"),
		Limit:       httpserver.ParseIntDefault(q.Get("limit"), 50),
		Offset:      httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	if s := q.Get("customer_id"); s != "" {
		u, err := parseUUID(s, "customer_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.CustomerID = &u
	}
	if s := q.Get("ticket_id"); s != "" {
		u, err := parseUUID(s, "ticket_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.TicketID = &u
	}
	items, total, err := h.srs.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]serviceRequestDTO, 0, len(items))
	for _, sr := range items {
		out = append(out, toSRDTO(sr))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": total,
	})
}

func (h *Handler) getServiceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "service_request")
	if err != nil {
		writeErr(w, err)
		return
	}
	sr, err := h.srs.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSRDTO(*sr))
}

func (h *Handler) approveServiceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "service_request")
	if err != nil {
		writeErr(w, err)
		return
	}
	by := actorUserID(r.Context())
	sr, err := h.srs.Approve(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSRDTO(*sr))
}

type rejectSRRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) rejectServiceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "service_request")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req rejectSRRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by := actorUserID(r.Context())
	sr, err := h.srs.Reject(r.Context(), id, by, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSRDTO(*sr))
}

func (h *Handler) startServiceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "service_request")
	if err != nil {
		writeErr(w, err)
		return
	}
	by := actorUserID(r.Context())
	sr, err := h.srs.StartFulfillment(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSRDTO(*sr))
}

func (h *Handler) fulfillServiceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "service_request")
	if err != nil {
		writeErr(w, err)
		return
	}
	by := actorUserID(r.Context())
	sr, err := h.srs.MarkFulfilled(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSRDTO(*sr))
}

type cancelSRRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) cancelServiceRequest(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "service_request")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req cancelSRRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by := actorUserID(r.Context())
	sr, err := h.srs.Cancel(r.Context(), id, by, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSRDTO(*sr))
}

// =====================================================================
// Teams
// =====================================================================

func (h *Handler) listTeams(w http.ResponseWriter, r *http.Request) {
	onlyActive := r.URL.Query().Get("only_active") != "false"
	items, err := h.teams.ListTeams(r.Context(), onlyActive)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]teamDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toTeamDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type createTeamRequest struct {
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	ManagerUserID    string   `json:"manager_user_id,omitempty"`
	FocusTicketTypes []string `json:"focus_ticket_types,omitempty"`
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	var req createTeamRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var managerPtr *uuid.UUID
	if req.ManagerUserID != "" {
		u, err := parseUUID(req.ManagerUserID, "manager_user_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		managerPtr = &u
	}
	focus := make([]domain.TicketType, 0, len(req.FocusTicketTypes))
	for _, s := range req.FocusTicketTypes {
		focus = append(focus, domain.TicketType(s))
	}
	t, err := h.teams.CreateTeam(r.Context(), req.Name, req.Description, managerPtr, focus)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTeamDTO(*t))
}

func (h *Handler) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	teamID, err := parseUUID(chi.URLParam(r, "id"), "team")
	if err != nil {
		writeErr(w, err)
		return
	}
	includeLeft := r.URL.Query().Get("include_left") == "true"
	items, err := h.teams.ListMembers(r.Context(), teamID, includeLeft)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]teamMemberDTO, 0, len(items))
	for _, m := range items {
		out = append(out, toTeamMemberDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type addTeamMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role,omitempty"`
}

func (h *Handler) addTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID, err := parseUUID(chi.URLParam(r, "id"), "team")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req addTeamMemberRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	userID, err := parseUUID(req.UserID, "user_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	role := domain.TeamMemberRole(req.Role)
	if role == "" {
		role = domain.TeamRoleAgent
	}
	m, err := h.teams.AddMember(r.Context(), teamID, userID, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTeamMemberDTO(*m))
}

func (h *Handler) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID, err := parseUUID(chi.URLParam(r, "id"), "team")
	if err != nil {
		writeErr(w, err)
		return
	}
	userID, err := parseUUID(chi.URLParam(r, "user_id"), "user_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.teams.RemoveMember(r.Context(), teamID, userID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type assignToTeamRequest struct {
	TeamID string `json:"team_id"`
	Mode   string `json:"mode,omitempty"` // "" or "round_robin"
}

func (h *Handler) assignTicketToTeam(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req assignToTeamRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	teamID, err := parseUUID(req.TeamID, "team_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.teams.AssignTicketToTeam(r.Context(), ticketID, teamID, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

func (h *Handler) assignTicketRoundRobin(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req assignToTeamRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	teamID, err := parseUUID(req.TeamID, "team_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.teams.RoundRobinAssign(r.Context(), ticketID, teamID, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

// =====================================================================
// WO from Ticket
// =====================================================================

type createWOFromTicketRequest struct {
	WOTemplateID string `json:"wo_template_id,omitempty"`
	ScheduledAt  string `json:"scheduled_at,omitempty"`
}

func (h *Handler) createWOFromTicket(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req createWOFromTicketRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var tmplPtr *uuid.UUID
	if req.WOTemplateID != "" {
		u, err := parseUUID(req.WOTemplateID, "wo_template_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		tmplPtr = &u
	}
	var sched *time.Time
	if req.ScheduledAt != "" {
		if t, err := time.Parse(time.RFC3339, req.ScheduledAt); err == nil {
			s := t.UTC()
			sched = &s
		}
	}
	by, role := actorWithRole(r.Context())
	woID, err := h.wft.CreateWO(r.Context(), ticketID, tmplPtr, sched, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"wo_id":    woID.String(),
		"ticket_id": ticketID.String(),
	})
}

// =====================================================================
// CSAT
// =====================================================================

type csatInviteRequest struct {
	Channel string `json:"channel,omitempty"`
}

func (h *Handler) sendCSATInvite(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req csatInviteRequest
	_ = httpserver.DecodeJSON(r, &req)
	if err := h.csat.SendInvite(r.Context(), id, req.Channel); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "sent"})
}

type csatResponseRequest struct {
	Rating  int    `json:"rating"`
	Comment string `json:"comment,omitempty"`
	Channel string `json:"channel,omitempty"`
}

func (h *Handler) recordCSATResponse(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req csatResponseRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	resp, err := h.csat.RecordResponse(r.Context(), id, req.Rating, req.Comment, req.Channel)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCSATDTO(*resp))
}

func (h *Handler) getTicketCSAT(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	c, err := h.csat.GetByTicket(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCSATDTO(*c))
}

func (h *Handler) csatAggregations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f port.CSATAggregationFilter
	if s := q.Get("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			tu := t.UTC()
			f.From = &tu
		}
	}
	if s := q.Get("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			tu := t.UTC()
			f.To = &tu
		}
	}
	agg, err := h.csat.Aggregations(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agg)
}

// =====================================================================
// Communications
// =====================================================================

type logOutboundCommRequest struct {
	Kind              string                  `json:"kind"`
	CounterpartyKind  string                  `json:"counterparty_kind,omitempty"`
	CounterpartyID    string                  `json:"counterparty_id,omitempty"`
	CounterpartyLabel string                  `json:"counterparty_label,omitempty"`
	Subject           string                  `json:"subject,omitempty"`
	Body              string                  `json:"body"`
	Attachments       []domain.CommAttachment `json:"attachments,omitempty"`
}

func (h *Handler) logOutboundComm(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req logOutboundCommRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	if by == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	var counterpartyPtr *uuid.UUID
	if req.CounterpartyID != "" {
		u, err := parseUUID(req.CounterpartyID, "counterparty_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		counterpartyPtr = &u
	}
	kind := domain.CounterpartyKind(req.CounterpartyKind)
	if kind == "" {
		kind = domain.CounterpartyCustomer
	}
	c, err := h.comm.LogOutbound(r.Context(), port.LogOutboundInput{
		TicketID:          ticketID,
		Kind:              domain.CommunicationKind(req.Kind),
		CounterpartyKind:  kind,
		CounterpartyID:    counterpartyPtr,
		CounterpartyLabel: req.CounterpartyLabel,
		Subject:           req.Subject,
		Body:              req.Body,
		Attachments:       req.Attachments,
		ByUserID:          by,
		ActorRole:         role,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCommunicationDTO(*c))
}

func (h *Handler) listTicketComm(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("limit"), 100)
	offset := httpserver.ParseIntDefault(q.Get("offset"), 0)
	items, err := h.comm.List(r.Context(), ticketID, limit, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]communicationDTO, 0, len(items))
	for _, c := range items {
		out = append(out, toCommunicationDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// whatsappWebhook is the WhatsApp gateway entrypoint. Wave 124 keeps
// the body decoding simple — the real signature verification + retry
// logic ships in Wave 127. For now the gateway's bearer token gates
// the route at the API gateway layer; we just persist the inbound row.
type whatsappWebhookRequest struct {
	From              string                  `json:"from"`              // msisdn
	ExternalMessageID string                  `json:"external_message_id"`
	Subject           string                  `json:"subject,omitempty"`
	Body              string                  `json:"body"`
	TicketID          string                  `json:"ticket_id,omitempty"`
	Attachments       []domain.CommAttachment `json:"attachments,omitempty"`
}

func (h *Handler) whatsappWebhook(w http.ResponseWriter, r *http.Request) {
	var req whatsappWebhookRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var ticketPtr *uuid.UUID
	if req.TicketID != "" {
		u, err := parseUUID(req.TicketID, "ticket_id")
		if err == nil {
			ticketPtr = &u
		}
	}
	c, err := h.comm.LogInbound(r.Context(), port.LogInboundInput{
		Kind:              domain.CommKindWhatsAppIn,
		ExternalMessageID: req.ExternalMessageID,
		TicketID:          ticketPtr,
		CounterpartyLabel: req.From,
		Subject:           req.Subject,
		Body:              req.Body,
		Attachments:       req.Attachments,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toCommunicationDTO(*c))
}
