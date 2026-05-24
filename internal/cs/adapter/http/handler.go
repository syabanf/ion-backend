// Package http is the driving adapter for the customer-service
// bounded context.
//
// Routes (see Mount for the full list):
//
//	Tickets   — /api/cs/tickets[, /{id}/{transition}]
//	Comments  — /api/cs/tickets/{id}/comments + /api/cs/comments/{id}
//	Mentions  — /api/cs/mentions + /api/cs/mentions/{id}/read
//	Channels  — /api/cs/channels (admin)
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	csusecase "github.com/ion-core/backend/internal/cs/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler is the CS HTTP surface.
type Handler struct {
	tickets  port.TicketUseCase
	comments port.CommentUseCase
	mentions port.MentionUseCase
	channels port.ChannelUseCase
	verifier *auth.Verifier

	// Wave 124 add-ons. nil-safe — Mount only attaches the relevant
	// route groups when the corresponding usecase is wired.
	sla    port.SLAUseCase
	srs    port.ServiceRequestUseCase
	teams  port.TeamUseCase
	wft    port.WOFromTicketUseCase
	csat   port.CSATUseCase
	comm   port.CommunicationUseCase

	// Wave 126 add-on. nil-safe — MountWave126 only attaches routes
	// when the dashboard service is wired.
	dashboard *csusecase.CSDashboardService

	// Wave 128D add-on. nil-safe — MountWave128D only attaches the
	// importer route when the service is wired.
	importer *csusecase.TicketImporterService
}

func NewHandler(
	tickets port.TicketUseCase,
	comments port.CommentUseCase,
	mentions port.MentionUseCase,
	channels port.ChannelUseCase,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		tickets:  tickets,
		comments: comments,
		mentions: mentions,
		channels: channels,
		verifier: verifier,
	}
}

// WithSLA wires the SLA usecase. nil-safe.
func (h *Handler) WithSLA(s port.SLAUseCase) *Handler { h.sla = s; return h }

// WithServiceRequests wires the service-request usecase.
func (h *Handler) WithServiceRequests(s port.ServiceRequestUseCase) *Handler { h.srs = s; return h }

// WithTeams wires the team usecase.
func (h *Handler) WithTeams(t port.TeamUseCase) *Handler { h.teams = t; return h }

// WithWOFromTicket wires the WO-from-Ticket usecase.
func (h *Handler) WithWOFromTicket(w port.WOFromTicketUseCase) *Handler { h.wft = w; return h }

// WithCSAT wires the CSAT usecase.
func (h *Handler) WithCSAT(c port.CSATUseCase) *Handler { h.csat = c; return h }

// WithCommunications wires the communication usecase.
func (h *Handler) WithCommunications(c port.CommunicationUseCase) *Handler { h.comm = c; return h }

// Mount attaches the CS routes to r. RequireAuth + per-route
// RequirePermission enforce the permission gates.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/cs", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Tickets
		r.With(httpserver.RequirePermission("cs.ticket.read")).Get("/tickets", h.listTickets)
		r.With(httpserver.RequirePermission("cs.ticket.write")).Post("/tickets", h.createTicket)
		r.With(httpserver.RequirePermission("cs.ticket.read")).Get("/tickets/{id}", h.getTicket)
		r.With(httpserver.RequirePermission("cs.ticket.assign")).Post("/tickets/{id}/assign", h.assignTicket)
		r.With(httpserver.RequirePermission("cs.ticket.write")).Post("/tickets/{id}/start", h.startTicket)
		r.With(httpserver.RequirePermission("cs.ticket.write")).Post("/tickets/{id}/pause", h.pauseTicket)
		r.With(httpserver.RequirePermission("cs.ticket.write")).Post("/tickets/{id}/resume", h.resumeTicket)
		r.With(httpserver.RequirePermission("cs.ticket.write")).Post("/tickets/{id}/resolve", h.resolveTicket)
		r.With(httpserver.RequirePermission("cs.ticket.close")).Post("/tickets/{id}/close", h.closeTicket)
		r.With(httpserver.RequirePermission("cs.ticket.reopen")).Post("/tickets/{id}/reopen", h.reopenTicket)
		r.With(httpserver.RequirePermission("cs.ticket.write")).Post("/tickets/{id}/priority", h.changePriority)
		r.With(httpserver.RequirePermission("cs.ticket.read")).Get("/tickets/{id}/events", h.listEvents)

		// Comments
		r.With(httpserver.RequirePermission("cs.comment.read")).Get("/tickets/{id}/comments", h.listComments)
		r.With(httpserver.RequirePermission("cs.comment.write")).Post("/tickets/{id}/comments", h.addComment)
		r.With(httpserver.RequirePermission("cs.comment.edit")).Patch("/comments/{id}", h.editComment)
		r.With(httpserver.RequirePermission("cs.comment.delete")).Delete("/comments/{id}", h.deleteComment)

		// Mentions
		r.With(httpserver.RequirePermission("cs.mention.read")).Get("/mentions", h.listMyMentions)
		r.With(httpserver.RequirePermission("cs.mention.read")).Post("/mentions/{id}/read", h.markMentionRead)

		// Channels (admin)
		r.With(httpserver.RequirePermission("cs.channel.read")).Get("/channels", h.listChannels)
		r.With(httpserver.RequirePermission("cs.channel.manage")).Post("/channels", h.createChannel)
		r.With(httpserver.RequirePermission("cs.channel.manage")).Patch("/channels/{code}", h.updateChannel)

		// =============================================================
		// Wave 124 — SLA + Service Requests + Teams + WO-from-Ticket +
		// CSAT + Communications
		// =============================================================
		if h.sla != nil {
			r.With(httpserver.RequirePermission("cs.sla.read")).Get("/sla/matrix", h.listSLAMatrix)
			r.With(httpserver.RequirePermission("cs.sla.manage")).Post("/sla/matrix", h.upsertSLAMatrix)
			r.With(httpserver.RequirePermission("cs.sla.read")).Get("/tickets/{id}/sla", h.getTicketSLA)
		}
		if h.srs != nil {
			r.With(httpserver.RequirePermission("cs.service_request.submit")).Post("/service-requests", h.submitServiceRequest)
			r.With(httpserver.RequirePermission("cs.service_request.read")).Get("/service-requests", h.listServiceRequests)
			r.With(httpserver.RequirePermission("cs.service_request.read")).Get("/service-requests/{id}", h.getServiceRequest)
			r.With(httpserver.RequirePermission("cs.service_request.approve")).Post("/service-requests/{id}/approve", h.approveServiceRequest)
			r.With(httpserver.RequirePermission("cs.service_request.reject")).Post("/service-requests/{id}/reject", h.rejectServiceRequest)
			r.With(httpserver.RequirePermission("cs.service_request.fulfill")).Post("/service-requests/{id}/start", h.startServiceRequest)
			r.With(httpserver.RequirePermission("cs.service_request.fulfill")).Post("/service-requests/{id}/fulfill", h.fulfillServiceRequest)
			r.With(httpserver.RequirePermission("cs.service_request.fulfill")).Post("/service-requests/{id}/cancel", h.cancelServiceRequest)
		}
		if h.teams != nil {
			r.With(httpserver.RequirePermission("cs.team.read")).Get("/teams", h.listTeams)
			r.With(httpserver.RequirePermission("cs.team.manage")).Post("/teams", h.createTeam)
			r.With(httpserver.RequirePermission("cs.team.read")).Get("/teams/{id}/members", h.listTeamMembers)
			r.With(httpserver.RequirePermission("cs.team.manage")).Post("/teams/{id}/members", h.addTeamMember)
			r.With(httpserver.RequirePermission("cs.team.manage")).Delete("/teams/{id}/members/{user_id}", h.removeTeamMember)
			r.With(httpserver.RequirePermission("cs.team.assign")).Post("/tickets/{id}/assign-team", h.assignTicketToTeam)
			r.With(httpserver.RequirePermission("cs.team.assign")).Post("/tickets/{id}/assign-round-robin", h.assignTicketRoundRobin)
		}
		if h.wft != nil {
			r.With(httpserver.RequirePermission("cs.wo.create_from_ticket")).Post("/tickets/{id}/create-wo", h.createWOFromTicket)
		}
		if h.csat != nil {
			r.With(httpserver.RequirePermission("cs.csat.read")).Get("/tickets/{id}/csat", h.getTicketCSAT)
			r.With(httpserver.RequirePermission("cs.csat.read")).Post("/tickets/{id}/csat-invite", h.sendCSATInvite)
			r.With(httpserver.RequirePermission("cs.csat.submit")).Post("/tickets/{id}/csat-response", h.recordCSATResponse)
			r.With(httpserver.RequirePermission("cs.csat.read")).Get("/csat/aggregations", h.csatAggregations)
		}
		if h.comm != nil {
			r.With(httpserver.RequirePermission("cs.communication.send")).Post("/tickets/{id}/communications", h.logOutboundComm)
			r.With(httpserver.RequirePermission("cs.communication.read")).Get("/tickets/{id}/communications", h.listTicketComm)
			// Webhook is permission-free — the gateway authenticates via
			// the bearer token shared with the inbound WhatsApp / email
			// poller. The actual sig verification lives in the producer.
			r.Post("/webhooks/whatsapp", h.whatsappWebhook)
		}
	})
}

// =====================================================================
// Tickets
// =====================================================================

type createTicketRequest struct {
	CustomerID     string         `json:"customer_id"`
	OpenedVia      string         `json:"opened_via"`
	TicketType     string         `json:"ticket_type"`
	Title          string         `json:"title"`
	Description    string         `json:"description,omitempty"`
	Priority       string         `json:"priority,omitempty"`
	SourceMetadata map[string]any `json:"source_metadata,omitempty"`
}

func (h *Handler) createTicket(w http.ResponseWriter, r *http.Request) {
	var req createTicketRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	customerID, err := parseUUID(req.CustomerID, "customer_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	openedBy := actorUserID(r.Context())
	if openedBy == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	priority := domain.Priority(strings.TrimSpace(req.Priority))
	if priority == "" {
		priority = domain.PriorityNormal
	}
	t, err := h.tickets.CreateTicket(r.Context(), port.CreateTicketInput{
		CustomerID:     customerID,
		OpenedBy:       openedBy,
		OpenedVia:      domain.OpenedVia(req.OpenedVia),
		TicketType:     domain.TicketType(req.TicketType),
		Title:          req.Title,
		Description:    req.Description,
		Priority:       priority,
		SourceMetadata: req.SourceMetadata,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTicketDTO(*t))
}

func (h *Handler) listTickets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.TicketListFilter{
		Status:     q.Get("status"),
		Priority:   q.Get("priority"),
		TicketType: q.Get("ticket_type"),
		OpenedVia:  q.Get("opened_via"),
		Limit:      pageSize,
		Offset:     (page - 1) * pageSize,
	}
	if q.Get("only_unassigned") == "true" {
		f.OnlyUnassigned = true
	}
	if s := q.Get("customer_id"); s != "" {
		u, err := parseUUID(s, "customer_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.CustomerID = &u
	}
	if s := q.Get("assigned_user_id"); s != "" {
		u, err := parseUUID(s, "assigned_user_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.AssignedUserID = &u
	}
	if s := q.Get("assigned_team_id"); s != "" {
		u, err := parseUUID(s, "assigned_team_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.AssignedTeamID = &u
	}
	items, total, err := h.tickets.ListTickets(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]ticketDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toTicketDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := h.tickets.GetTicket(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

type assignTicketRequest struct {
	AssignedUserID string `json:"assigned_user_id"`
}

func (h *Handler) assignTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req assignTicketRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	assignedUserID, err := parseUUID(req.AssignedUserID, "assigned_user_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.AssignTicket(r.Context(), id, assignedUserID, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

func (h *Handler) startTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.StartTicket(r.Context(), id, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

type pauseTicketRequest struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

func (h *Handler) pauseTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req pauseTicketRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.PauseTicket(r.Context(), id, domain.PauseKind(req.Kind), req.Reason, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

func (h *Handler) resumeTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.ResumeTicket(r.Context(), id, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

type resolveTicketRequest struct {
	Resolution string `json:"resolution"`
}

func (h *Handler) resolveTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req resolveTicketRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.ResolveTicket(r.Context(), id, req.Resolution, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

func (h *Handler) closeTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.CloseTicket(r.Context(), id, by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

type reopenTicketRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (h *Handler) reopenTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req reopenTicketRequest
	_ = httpserver.DecodeJSON(r, &req)
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.ReopenTicket(r.Context(), id, by, req.Reason, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

type changePriorityRequest struct {
	Priority string `json:"priority"`
}

func (h *Handler) changePriority(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req changePriorityRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	t, err := h.tickets.ChangePriority(r.Context(), id, domain.Priority(req.Priority), by, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTicketDTO(*t))
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("limit"), 100)
	offset := httpserver.ParseIntDefault(q.Get("offset"), 0)
	events, err := h.tickets.ListEvents(r.Context(), id, limit, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]ticketEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, toTicketEventDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =====================================================================
// Comments
// =====================================================================

type createCommentRequest struct {
	Body        string                     `json:"body"`
	IsInternal  bool                       `json:"is_internal,omitempty"`
	Attachments []domain.CommentAttachment `json:"attachments,omitempty"`
}

func (h *Handler) addComment(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req createCommentRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	authorID := actorUserID(r.Context())
	if authorID == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	_, role := actorWithRole(r.Context())
	c, mentions, err := h.comments.AddComment(r.Context(), port.CreateCommentInput{
		TicketID:    ticketID,
		AuthorID:    authorID,
		AuthorRole:  role,
		Body:        req.Body,
		IsInternal:  req.IsInternal,
		Attachments: req.Attachments,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"comment":  toCommentDTO(*c),
		"mentions": toMentionDTOs(mentions),
	})
}

type editCommentRequest struct {
	Body string `json:"body"`
}

func (h *Handler) editComment(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "comment")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req editCommentRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	c, err := h.comments.EditComment(r.Context(), id, req.Body, by, isSupervisor(role))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCommentDTO(*c))
}

func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "comment")
	if err != nil {
		writeErr(w, err)
		return
	}
	by, role := actorWithRole(r.Context())
	if err := h.comments.DeleteComment(r.Context(), id, by, isSupervisor(role)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listComments(w http.ResponseWriter, r *http.Request) {
	ticketID, err := parseUUID(chi.URLParam(r, "id"), "ticket")
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	f := port.CommentListFilter{
		TicketID:        ticketID,
		IncludeDeleted:  q.Get("include_deleted") == "true",
		IncludeInternal: q.Get("include_internal") != "false",
		Limit:           httpserver.ParseIntDefault(q.Get("limit"), 100),
		Offset:          httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	items, err := h.comments.ListComments(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]commentDTO, 0, len(items))
	for _, c := range items {
		out = append(out, toCommentDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =====================================================================
// Mentions
// =====================================================================

func (h *Handler) listMyMentions(w http.ResponseWriter, r *http.Request) {
	userID := actorUserID(r.Context())
	if userID == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	q := r.URL.Query()
	f := port.MentionListFilter{
		MentionedUserID: userID,
		UnreadOnly:      q.Get("unread_only") == "true",
		Limit:           httpserver.ParseIntDefault(q.Get("limit"), 50),
		Offset:          httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	if s := q.Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.Since = &t
		}
	}
	items, err := h.mentions.ListMyMentions(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": toMentionDTOs(items),
	})
}

func (h *Handler) markMentionRead(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "mention")
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := actorUserID(r.Context())
	if userID == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.mentions.MarkAsRead(r.Context(), id, userID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// Channels (admin)
// =====================================================================

type createChannelRequest struct {
	Code     string         `json:"code"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	IsActive bool           `json:"is_active"`
	Config   map[string]any `json:"config,omitempty"`
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	onlyActive := r.URL.Query().Get("only_active") == "true"
	items, err := h.channels.ListChannels(r.Context(), onlyActive)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]channelDTO, 0, len(items))
	for _, c := range items {
		out = append(out, toChannelDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	c, err := h.channels.CreateChannel(r.Context(), req.Code, req.Name, domain.ChannelKind(req.Kind), req.IsActive, req.Config)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toChannelDTO(*c))
}

type updateChannelRequest struct {
	Name     *string        `json:"name,omitempty"`
	Kind     *string        `json:"kind,omitempty"`
	IsActive *bool          `json:"is_active,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if code == "" {
		writeErr(w, errors.Validation("cs.channel.code_required", "channel code is required"))
		return
	}
	var req updateChannelRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var kindPtr *domain.ChannelKind
	if req.Kind != nil {
		k := domain.ChannelKind(*req.Kind)
		kindPtr = &k
	}
	c, err := h.channels.UpdateChannel(r.Context(), code, req.Name, kindPtr, req.IsActive, req.Config)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toChannelDTO(*c))
}

// =====================================================================
// Helpers
// =====================================================================

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return uuid.Nil, errors.Validation(field+".id_invalid", field+" is not a valid uuid")
	}
	return id, nil
}

// actorUserID returns the authenticated user's UUID or uuid.Nil.
func actorUserID(ctx context.Context) uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return uuid.Nil
	}
	return c.UserID
}

// actorWithRole returns (userID, primary role name). The role name is
// best-effort: the first role in the claims. Used as actor_role on
// audit events.
func actorWithRole(ctx context.Context) (uuid.UUID, string) {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return uuid.Nil, ""
	}
	role := ""
	if len(c.Roles) > 0 {
		role = c.Roles[0]
	}
	return c.UserID, role
}

// isSupervisor checks the role string for the cs_supervisor / super_admin
// elevation. The check is intentionally loose — the permission gates on
// the HTTP route already gate access.
func isSupervisor(role string) bool {
	switch role {
	case "cs_supervisor", "super_admin", "admin":
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpserver.WriteJSON(w, status, body)
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}

// rawJSON wraps a json.RawMessage for DTO embedding.
type rawJSON = json.RawMessage
