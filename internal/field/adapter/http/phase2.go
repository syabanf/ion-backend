// Phase 2 field endpoints — CS tickets + maintenance events. Same
// quick-delivery approach as crm-phase2: direct pgxpool, no
// hexagonal layering until MVP volumes prove out.
package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Phase2Handler exposes:
//
//	Tickets
//	  GET  /tickets                     — list tickets (filterable by status / assigned_to_me)
//	  GET  /tickets/{id}                — fetch one
//	  POST /tickets                     — open a ticket (CS agent)
//	  PATCH /tickets/{id}               — assign / status / resolve
//
//	Maintenance events
//	  GET  /maintenance-events          — list events (filterable by status)
//	  GET  /maintenance-events/{id}     — fetch one (with covered nodes)
//	  POST /maintenance-events          — create event
type Phase2Handler struct {
	pool     *pgxpool.Pool
	verifier *auth.Verifier
}

func NewPhase2Handler(pool *pgxpool.Pool, verifier *auth.Verifier) *Phase2Handler {
	return &Phase2Handler{pool: pool, verifier: verifier}
}

func (h *Phase2Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Tickets
		r.With(httpserver.RequirePermission("field.ticket.read")).
			Get("/tickets", h.listTickets)
		r.With(httpserver.RequirePermission("field.ticket.read")).
			Get("/tickets/{id}", h.getTicket)
		r.With(httpserver.RequirePermission("field.ticket.read")).
			Get("/tickets/{id}/messages", h.listTicketMessages)
		r.With(httpserver.RequirePermission("field.ticket.reply")).
			Post("/tickets/{id}/messages", h.postTicketMessageAgent)
		r.With(httpserver.RequirePermission("field.ticket.create")).
			Post("/tickets", h.createTicket)
		r.With(httpserver.RequirePermission("field.ticket.resolve")).
			Patch("/tickets/{id}", h.patchTicket)

		// Tech-app "Start Journey" + "Arrived" timestamps (PRD §6.1).
		r.Post("/work-orders/{id}/journey/start", h.startJourney)
		r.Post("/work-orders/{id}/journey/arrived", h.markArrived)

		// Live GPS streaming — tech posts pings, Team Leader reads.
		r.With(httpserver.RequirePermission("field.tech_location.write")).
			Post("/tech-locations", h.postTechLocation)
		r.With(httpserver.RequirePermission("field.tech_location.read")).
			Get("/tech-locations", h.listTechLocations)
		r.With(httpserver.RequirePermission("field.tech_location.read")).
			Get("/work-orders/{id}/tech-locations", h.listWOTechLocations)

		// Cross-area dispatch borrow flow.
		r.With(httpserver.RequirePermission("field.cross_area.request")).
			Post("/work-orders/{id}/cross-area", h.requestCrossArea)
		r.With(httpserver.RequirePermission("field.cross_area.request")).
			Get("/cross-area/pending", h.listCrossAreaPending)

		// Auto-pair suggestion for SLA-breached WOs.
		r.With(httpserver.RequirePermission("field.wo.assign")).
			Get("/work-orders/{id}/suggested-pair", h.suggestedPair)

		// Mid-schedule priority insertion.
		r.With(httpserver.RequirePermission("field.priority_insert")).
			Post("/work-orders/{id}/priority-insert", h.priorityInsertWO)
		r.Get("/priority-insertions/mine", h.myPriorityInsertions)
		r.Post("/priority-insertions/{id}/respond", h.respondPriorityInsertion)

		// Ticket attachments (extend existing message POST).
		r.With(httpserver.RequirePermission("field.ticket.read")).
			Get("/tickets/{id}/attachments", h.listTicketAttachments)

		// Speedtest parser — decodes the pipe-encoded response_text
		// stored by the tech app's "speedtest" checklist item into
		// structured down/up/ping fields for the dashboard.
		r.With(httpserver.RequirePermission("field.wo.read")).
			Get("/work-orders/{id}/speedtest", h.getWorkOrderSpeedtest)
		r.With(httpserver.RequirePermission("field.wo.read")).
			Get("/speedtests/recent", h.listRecentSpeedtests)

		// Maintenance events
		r.With(httpserver.RequirePermission("field.maintenance.read")).
			Get("/maintenance-events", h.listMaintenanceEvents)
		r.With(httpserver.RequirePermission("field.maintenance.read")).
			Get("/maintenance-events/{id}", h.getMaintenanceEvent)
		r.With(httpserver.RequirePermission("field.maintenance.create")).
			Post("/maintenance-events", h.createMaintenanceEvent)
		r.With(httpserver.RequirePermission("field.maintenance.dispatch")).
			Post("/maintenance-events/{id}/dispatch", h.dispatchMaintenanceEvent)
	})
}

// =============================================================================
// Tickets
// =============================================================================

type ticketDTO struct {
	ID            string     `json:"id"`
	TicketNumber  string     `json:"ticket_number"`
	CustomerID    string     `json:"customer_id"`
	Category      string     `json:"category"`
	Priority      string     `json:"priority"`
	Status        string     `json:"status"`
	Summary       string     `json:"summary"`
	Description   string     `json:"description,omitempty"`
	AssignedTo    *string    `json:"assigned_to,omitempty"`
	WoID          *string    `json:"wo_id,omitempty"`
	OpenedAt      time.Time  `json:"opened_at"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	CSATScore     *int       `json:"csat_score,omitempty"`
}

func (h *Phase2Handler) listTickets(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	mine := r.URL.Query().Get("assigned_to_me") == "true"
	claims := httpserver.ClaimsFromContext(r.Context())
	q := `
		SELECT id, ticket_number, customer_id, category, priority, status,
		       summary, COALESCE(description,''), assigned_to, wo_id,
		       created_at, resolved_at, csat_score
		FROM field.tickets
		WHERE 1=1
	`
	args := []any{}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if mine && claims != nil {
		args = append(args, claims.UserID)
		q += fmt.Sprintf(" AND assigned_to = $%d", len(args))
	}
	q += " ORDER BY created_at DESC LIMIT 200"

	rows, err := h.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []ticketDTO{}
	for rows.Next() {
		var t ticketDTO
		var assigned, wo *uuid.UUID
		if err := rows.Scan(&t.ID, &t.TicketNumber, &t.CustomerID, &t.Category, &t.Priority,
			&t.Status, &t.Summary, &t.Description, &assigned, &wo,
			&t.OpenedAt, &t.ResolvedAt, &t.CSATScore); err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
		if assigned != nil {
			s := assigned.String()
			t.AssignedTo = &s
		}
		if wo != nil {
			s := wo.String()
			t.WoID = &s
		}
		out = append(out, t)
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Phase2Handler) getTicket(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var t ticketDTO
	var assigned, wo *uuid.UUID
	if err := h.pool.QueryRow(r.Context(), `
		SELECT id, ticket_number, customer_id, category, priority, status,
		       summary, COALESCE(description,''), assigned_to, wo_id,
		       created_at, resolved_at, csat_score
		FROM field.tickets WHERE id = $1
	`, id).Scan(&t.ID, &t.TicketNumber, &t.CustomerID, &t.Category, &t.Priority,
		&t.Status, &t.Summary, &t.Description, &assigned, &wo, &t.OpenedAt,
		&t.ResolvedAt, &t.CSATScore); err != nil {
		if err == pgx.ErrNoRows {
			writeFieldErr(w, http.StatusNotFound, fieldErrMsg{"ticket not found"})
			return
		}
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	if assigned != nil {
		s := assigned.String()
		t.AssignedTo = &s
	}
	if wo != nil {
		s := wo.String()
		t.WoID = &s
	}
	writeFieldJSON(w, http.StatusOK, t)
}

type createTicketInput struct {
	CustomerID  string `json:"customer_id"`
	Category    string `json:"category"`
	Priority    string `json:"priority"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
}

func (h *Phase2Handler) createTicket(w http.ResponseWriter, r *http.Request) {
	var in createTicketInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Summary == "" || in.Category == "" {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"summary and category are required"})
		return
	}
	customerID, err := uuid.Parse(in.CustomerID)
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var openedBy *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		openedBy = &uid
	}
	id := uuid.New()
	// Ticket number — TKT-YYYYMMDD-<id-prefix>; cheap and human-readable.
	ticketNo := fmt.Sprintf("TKT-%s-%s", time.Now().Format("20060102"), id.String()[:8])
	// SLA snapshot — 1 hr response / 4 hr resolve for high; 4 hr / 24 hr
	// for medium; 24 hr / 72 hr for low. Trivial defaults; admin UI will
	// own the matrix later.
	var responseHrs, resolveHrs int
	switch in.Priority {
	case "high":
		responseHrs, resolveHrs = 1, 4
	case "low":
		responseHrs, resolveHrs = 24, 72
	default:
		responseHrs, resolveHrs = 4, 24
	}
	now := time.Now()
	respDue := now.Add(time.Duration(responseHrs) * time.Hour)
	resvDue := now.Add(time.Duration(resolveHrs) * time.Hour)

	// Equipment-damage tickets always need a field visit. We open a
	// maintenance WO atomically with the ticket and link them via
	// tickets.wo_id (FK) + work_orders.ticket_id (back-pointer). Other
	// categories (slow_speed, billing_dispute, etc.) are resolved
	// remotely by CS; if they later need a visit, the CS agent can
	// link a WO via PATCH manually.
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO field.tickets (
			id, ticket_number, customer_id, category, priority, status,
			summary, description, opened_by, sla_response_due, sla_resolve_due
		) VALUES ($1,$2,$3,$4,$5,'open',$6,NULLIF($7,''),$8,$9,$10)
	`, id, ticketNo, customerID, in.Category, in.Priority,
		in.Summary, in.Description, openedBy, respDue, resvDue); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}

	var autoWoID *uuid.UUID
	if in.Category == "equipment_damage" {
		var custAddress string
		_ = tx.QueryRow(ctx, `SELECT COALESCE(address,'') FROM crm.customers WHERE id = $1`, customerID).
			Scan(&custAddress)
		woID := uuid.New()
		woNumber := fmt.Sprintf("WO-%s-%s", time.Now().Format("20060102"), woID.String()[:8])
		if _, err := tx.Exec(ctx, `
			INSERT INTO field.work_orders (
				id, wo_number, customer_id, wo_type, product_type, address,
				status, priority, ticket_id, maintenance_subtype
			) VALUES ($1, $2, $3, 'maintenance', 'broadband', $4, 'unassigned',
			          $5, $6, 'equipment_damage')
		`, woID, woNumber, customerID,
			fallbackField(custAddress, "(address unknown)"), in.Priority, id); err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE field.tickets SET wo_id = $1 WHERE id = $2`, woID, id); err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
		autoWoID = &woID
	}

	if err := tx.Commit(ctx); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{
		"id":            id.String(),
		"ticket_number": ticketNo,
		"status":        "open",
	}
	if autoWoID != nil {
		resp["wo_id"] = autoWoID.String()
	}
	writeFieldJSON(w, http.StatusCreated, resp)
}

func fallbackField(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// =============================================================================
// dispatchMaintenanceEvent — assign a team + flip status to 'dispatched'.
// Optionally spawns a maintenance WO for each affected node so the field
// crew has individual tickets to close.
// =============================================================================

type dispatchMaintenanceInput struct {
	TeamID    string `json:"team_id"`
	OpenWOs   bool   `json:"open_wos,omitempty"` // true → spawn one WO per affected node
	Priority  string `json:"priority,omitempty"` // defaults to medium
}

func (h *Phase2Handler) dispatchMaintenanceEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var in dispatchMaintenanceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	teamID, err := uuid.Parse(in.TeamID)
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"team_id is required"})
		return
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback(ctx)

	// Flip the event.
	tag, err := tx.Exec(ctx, `
		UPDATE field.maintenance_events
		SET assigned_team_id = $1, status = 'dispatched', updated_at = NOW()
		WHERE id = $2 AND status = 'planned'
	`, teamID, id)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeFieldErr(w, http.StatusConflict, fieldErrMsg{"event already dispatched or not found"})
		return
	}

	// Optionally spawn one WO per affected node. The WO's customer_id
	// is a placeholder for now (maintenance WOs are network-scoped
	// rather than customer-scoped; the existing schema requires the
	// column, so we point it at the zero UUID and the field UI treats
	// it as "no customer"). Future migration can relax the NOT NULL.
	var spawned []string
	if in.OpenWOs {
		nodeRows, err := tx.Query(ctx,
			`SELECT node_id FROM field.maintenance_event_nodes WHERE event_id = $1`, id)
		if err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
		var nodeIDs []uuid.UUID
		for nodeRows.Next() {
			var n uuid.UUID
			if err := nodeRows.Scan(&n); err == nil {
				nodeIDs = append(nodeIDs, n)
			}
		}
		nodeRows.Close()
		// No nodes attached → spawn one WO for the whole event instead.
		if len(nodeIDs) == 0 {
			nodeIDs = []uuid.UUID{uuid.Nil}
		}
		for range nodeIDs {
			woID := uuid.New()
			woNumber := fmt.Sprintf("WO-%s-%s", time.Now().Format("20060102"), woID.String()[:8])
			// Placeholder customer + address for maintenance WOs.
			placeholderCust := uuid.Nil
			if _, err := tx.Exec(ctx, `
				INSERT INTO field.work_orders (
					id, wo_number, customer_id, wo_type, product_type, address,
					status, priority, team_id, maintenance_event_id,
					maintenance_subtype
				) VALUES ($1,$2,$3,'maintenance','broadband','(maintenance)',
				          'assigned',$4,$5,$6,'scheduled')
			`, woID, woNumber, placeholderCust, in.Priority, teamID, id); err != nil {
				writeFieldErr(w, http.StatusInternalServerError, err)
				return
			}
			spawned = append(spawned, woID.String())
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"status":      "dispatched",
		"team_id":     teamID.String(),
		"spawned_wos": spawned,
	})
}

type patchTicketInput struct {
	Status       *string `json:"status,omitempty"`
	AssignedTo   *string `json:"assigned_to,omitempty"`
	WoID         *string `json:"wo_id,omitempty"`
	CSATScore    *int    `json:"csat_score,omitempty"`
	CSATComment  *string `json:"csat_comment,omitempty"`
}

func (h *Phase2Handler) patchTicket(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var in patchTicketInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	// Build a partial update — only the fields that are not-nil.
	q := "UPDATE field.tickets SET updated_at = NOW()"
	args := []any{}
	if in.Status != nil {
		args = append(args, *in.Status)
		q += fmt.Sprintf(", status = $%d", len(args))
		if *in.Status == "resolved" {
			q += ", resolved_at = NOW()"
		}
		if *in.Status == "closed" {
			q += ", closed_at = NOW()"
		}
	}
	if in.AssignedTo != nil {
		uid, err := uuid.Parse(*in.AssignedTo)
		if err != nil {
			writeFieldErr(w, http.StatusBadRequest, err)
			return
		}
		args = append(args, uid)
		q += fmt.Sprintf(", assigned_to = $%d", len(args))
	}
	if in.WoID != nil {
		uid, err := uuid.Parse(*in.WoID)
		if err != nil {
			writeFieldErr(w, http.StatusBadRequest, err)
			return
		}
		args = append(args, uid)
		q += fmt.Sprintf(", wo_id = $%d", len(args))
	}
	if in.CSATScore != nil {
		args = append(args, *in.CSATScore)
		q += fmt.Sprintf(", csat_score = $%d", len(args))
	}
	if in.CSATComment != nil {
		args = append(args, *in.CSATComment)
		q += fmt.Sprintf(", csat_comment = $%d", len(args))
	}
	args = append(args, id)
	q += fmt.Sprintf(" WHERE id = $%d", len(args))

	if _, err := h.pool.Exec(r.Context(), q, args...); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Maintenance events
// =============================================================================

type maintEventDTO struct {
	ID             string     `json:"id"`
	EventCode      string     `json:"event_code"`
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	EventKind      string     `json:"event_kind"`
	ScheduledStart time.Time  `json:"scheduled_start"`
	ScheduledEnd   *time.Time `json:"scheduled_end,omitempty"`
	Status         string     `json:"status"`
	BranchID       *string    `json:"branch_id,omitempty"`
	AssignedTeamID *string    `json:"assigned_team_id,omitempty"`
	NodeCount      int        `json:"node_count"`
}

func (h *Phase2Handler) listMaintenanceEvents(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	q := `
		SELECT m.id, m.event_code, m.title, COALESCE(m.description,''),
		       m.event_kind, m.scheduled_start, m.scheduled_end, m.status,
		       m.branch_id, m.assigned_team_id,
		       (SELECT COUNT(*) FROM field.maintenance_event_nodes n WHERE n.event_id = m.id)
		FROM field.maintenance_events m
		WHERE 1=1
	`
	args := []any{}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND m.status = $%d", len(args))
	}
	q += " ORDER BY m.scheduled_start DESC LIMIT 200"

	rows, err := h.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []maintEventDTO{}
	for rows.Next() {
		var m maintEventDTO
		var branch, team *uuid.UUID
		if err := rows.Scan(&m.ID, &m.EventCode, &m.Title, &m.Description,
			&m.EventKind, &m.ScheduledStart, &m.ScheduledEnd, &m.Status,
			&branch, &team, &m.NodeCount); err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
		if branch != nil {
			s := branch.String()
			m.BranchID = &s
		}
		if team != nil {
			s := team.String()
			m.AssignedTeamID = &s
		}
		out = append(out, m)
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Phase2Handler) getMaintenanceEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var m maintEventDTO
	var branch, team *uuid.UUID
	if err := h.pool.QueryRow(r.Context(), `
		SELECT m.id, m.event_code, m.title, COALESCE(m.description,''),
		       m.event_kind, m.scheduled_start, m.scheduled_end, m.status,
		       m.branch_id, m.assigned_team_id,
		       (SELECT COUNT(*) FROM field.maintenance_event_nodes n WHERE n.event_id = m.id)
		FROM field.maintenance_events m WHERE id = $1
	`, id).Scan(&m.ID, &m.EventCode, &m.Title, &m.Description, &m.EventKind,
		&m.ScheduledStart, &m.ScheduledEnd, &m.Status, &branch, &team, &m.NodeCount); err != nil {
		if err == pgx.ErrNoRows {
			writeFieldErr(w, http.StatusNotFound, fieldErrMsg{"event not found"})
			return
		}
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	if branch != nil {
		s := branch.String()
		m.BranchID = &s
	}
	if team != nil {
		s := team.String()
		m.AssignedTeamID = &s
	}
	// Pull the node ids so the mobile app can show which ODPs are
	// affected — bare ids for now, the network-svc resolves to names
	// in a follow-up.
	nodeRows, _ := h.pool.Query(r.Context(),
		`SELECT node_id, COALESCE(note,'') FROM field.maintenance_event_nodes WHERE event_id = $1`, id)
	type nodeRef struct {
		NodeID string `json:"node_id"`
		Note   string `json:"note,omitempty"`
	}
	nodes := []nodeRef{}
	if nodeRows != nil {
		for nodeRows.Next() {
			var n nodeRef
			_ = nodeRows.Scan(&n.NodeID, &n.Note)
			nodes = append(nodes, n)
		}
		nodeRows.Close()
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{
		"event": m,
		"nodes": nodes,
	})
}

type createMaintEventInput struct {
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	EventKind      string    `json:"event_kind"`
	ScheduledStart time.Time `json:"scheduled_start"`
	ScheduledEnd   *time.Time `json:"scheduled_end,omitempty"`
	BranchID       *string   `json:"branch_id,omitempty"`
	NodeIDs        []string  `json:"node_ids,omitempty"`
}

func (h *Phase2Handler) createMaintenanceEvent(w http.ResponseWriter, r *http.Request) {
	var in createMaintEventInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Title == "" || in.EventKind == "" {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"title and event_kind are required"})
		return
	}
	id := uuid.New()
	code := fmt.Sprintf("MAINT-%s-%s", time.Now().Format("20060102"), id.String()[:8])
	var branchUUID *uuid.UUID
	if in.BranchID != nil {
		u, err := uuid.Parse(*in.BranchID)
		if err != nil {
			writeFieldErr(w, http.StatusBadRequest, err)
			return
		}
		branchUUID = &u
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO field.maintenance_events (
			id, event_code, title, description, event_kind,
			scheduled_start, scheduled_end, branch_id, status
		) VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,'planned')
	`, id, code, in.Title, in.Description, in.EventKind,
		in.ScheduledStart, in.ScheduledEnd, branchUUID); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, ns := range in.NodeIDs {
		nodeID, err := uuid.Parse(ns)
		if err != nil {
			writeFieldErr(w, http.StatusBadRequest, err)
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO field.maintenance_event_nodes (event_id, node_id)
			VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, id, nodeID); err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusCreated, map[string]any{
		"id":         id.String(),
		"event_code": code,
		"status":     "planned",
	})
}

// =============================================================================
// Ticket messages (staff side) + Journey timestamps
// =============================================================================

func (h *Phase2Handler) listTicketMessages(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	includeInternal := r.URL.Query().Get("include_internal") == "true"
	q := `SELECT id, author_kind, body, is_internal_note, created_at
	      FROM field.ticket_messages WHERE ticket_id = $1`
	if !includeInternal {
		q += " AND is_internal_note = FALSE"
	}
	q += " ORDER BY created_at ASC"
	rows, err := h.pool.Query(r.Context(), q, id)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID          string    `json:"id"`
		AuthorKind  string    `json:"author_kind"`
		Body        string    `json:"body"`
		Internal    bool      `json:"is_internal_note"`
		CreatedAt   time.Time `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.AuthorKind, &x.Body, &x.Internal, &x.CreatedAt); err != nil {
			writeFieldErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}

type agentMessageInput struct {
	Body        string   `json:"body"`
	Internal    bool     `json:"is_internal_note,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

func (h *Phase2Handler) postTicketMessageAgent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var in agentMessageInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Body == "" && len(in.Attachments) == 0 {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"body or attachments required"})
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var authorID *uuid.UUID
	if claims != nil {
		u := claims.UserID
		authorID = &u
	}
	atts, _ := json.Marshal(in.Attachments)
	if string(atts) == "null" {
		atts = []byte("[]")
	}
	msgID := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.ticket_messages
			(id, ticket_id, author_kind, author_user_id, body, is_internal_note, attachments)
		VALUES ($1, $2, 'agent', $3, $4, $5, $6::jsonb)
	`, msgID, id, authorID, in.Body, in.Internal, atts); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = h.pool.Exec(r.Context(),
		`UPDATE field.tickets SET last_message_at = NOW(), updated_at = NOW() WHERE id = $1`, id)
	writeFieldJSON(w, http.StatusCreated, map[string]any{"id": msgID.String()})
}

func (h *Phase2Handler) startJourney(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE field.work_orders
		SET journey_started_at = COALESCE(journey_started_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1 AND status IN ('assigned','dispatched')
	`, id)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeFieldErr(w, http.StatusConflict, fieldErrMsg{"WO must be assigned/dispatched"})
		return
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Phase2Handler) markArrived(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE field.work_orders
		SET arrived_at = COALESCE(arrived_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1
	`, id)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeFieldErr(w, http.StatusNotFound, fieldErrMsg{"WO not found"})
		return
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Small helpers — namespaced to avoid clashing with the original
// handler's writers if they exist.
// =============================================================================

type fieldErrMsg struct{ Message string }

func (e fieldErrMsg) Error() string { return e.Message }

// =============================================================================
// Live GPS streaming
//
// Tech app POSTs a ping every N seconds while a WO is in_progress.
// Team Leader dashboard polls /tech-locations?since=... to render a
// live map. We deliberately do NOT use WebSockets — polling at 15-30s
// is good enough for the deployment scale today (max ~50 techs per
// branch) and avoids a long-lived connection on the Go service.
// =============================================================================

type techLocationInput struct {
	WoID       string  `json:"wo_id,omitempty"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	AccuracyM  float64 `json:"accuracy_m,omitempty"`
	SpeedMps   float64 `json:"speed_mps,omitempty"`
	HeadingDeg float64 `json:"heading_deg,omitempty"`
	CapturedAt string  `json:"captured_at,omitempty"` // RFC3339; defaults to now
}

func (h *Phase2Handler) postTechLocation(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeFieldErr(w, http.StatusUnauthorized, fieldErrMsg{"no session"})
		return
	}
	var in techLocationInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Lat == 0 && in.Lng == 0 {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"lat/lng required"})
		return
	}
	var woUUID *uuid.UUID
	if in.WoID != "" {
		if u, err := uuid.Parse(in.WoID); err == nil {
			woUUID = &u
		}
	}
	capturedAt := time.Now()
	if in.CapturedAt != "" {
		if t, err := time.Parse(time.RFC3339, in.CapturedAt); err == nil {
			capturedAt = t
		}
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.tech_locations
			(user_id, wo_id, lat, lng,
			 accuracy_m, speed_mps, heading_deg, captured_at)
		VALUES ($1,$2,$3,$4,
		        NULLIF($5,0)::double precision,
		        NULLIF($6,0)::double precision,
		        NULLIF($7,0)::double precision,
		        $8)
	`, claims.UserID, woUUID, in.Lat, in.Lng,
		in.AccuracyM, in.SpeedMps, in.HeadingDeg, capturedAt); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// listTechLocations — Team Leader view.
//
// Query: ?since=RFC3339&active_only=true
//   active_only=true  → only techs with at least one ping in the
//                       last 10 minutes; LATERAL pulls the freshest
//                       ping per tech.
//   since=...         → only pings captured after this timestamp;
//                       used by the dashboard's polling loop.
func (h *Phase2Handler) listTechLocations(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") == "true"
	since := r.URL.Query().Get("since")

	q := `
		SELECT DISTINCT ON (l.user_id)
		       l.user_id, COALESCE(u.full_name, u.email, ''),
		       l.wo_id, l.lat, l.lng,
		       l.accuracy_m, l.speed_mps, l.heading_deg, l.captured_at
		FROM field.tech_locations l
		LEFT JOIN identity.users u ON u.id = l.user_id
		WHERE 1=1
	`
	args := []any{}
	if activeOnly {
		q += " AND l.captured_at > NOW() - INTERVAL '10 minutes'"
	}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			args = append(args, t)
			q += fmt.Sprintf(" AND l.captured_at > $%d", len(args))
		}
	}
	q += " ORDER BY l.user_id, l.captured_at DESC LIMIT 500"

	rows, err := h.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		UserID     string    `json:"user_id"`
		FullName   string    `json:"full_name"`
		WoID       *string   `json:"wo_id,omitempty"`
		Lat        float64   `json:"lat"`
		Lng        float64   `json:"lng"`
		AccuracyM  *float64  `json:"accuracy_m,omitempty"`
		SpeedMps   *float64  `json:"speed_mps,omitempty"`
		HeadingDeg *float64  `json:"heading_deg,omitempty"`
		CapturedAt time.Time `json:"captured_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var wo *uuid.UUID
		if err := rows.Scan(&x.UserID, &x.FullName, &wo, &x.Lat, &x.Lng,
			&x.AccuracyM, &x.SpeedMps, &x.HeadingDeg, &x.CapturedAt); err != nil {
			continue
		}
		if wo != nil {
			s := wo.String()
			x.WoID = &s
		}
		out = append(out, x)
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}

// listWOTechLocations — replay a single WO's journey (ordered by time).
func (h *Phase2Handler) listWOTechLocations(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT lat, lng, COALESCE(accuracy_m, 0), captured_at
		FROM field.tech_locations
		WHERE wo_id = $1
		ORDER BY captured_at ASC
		LIMIT 2000
	`, id)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type p struct {
		Lat        float64   `json:"lat"`
		Lng        float64   `json:"lng"`
		AccuracyM  float64   `json:"accuracy_m,omitempty"`
		CapturedAt time.Time `json:"captured_at"`
	}
	out := []p{}
	for rows.Next() {
		var x p
		if err := rows.Scan(&x.Lat, &x.Lng, &x.AccuracyM, &x.CapturedAt); err == nil {
			out = append(out, x)
		}
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"points": out})
}

func writeFieldJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeFieldErr(w http.ResponseWriter, status int, err error) {
	writeFieldJSON(w, status, map[string]any{
		"error": map[string]string{"message": err.Error()},
	})
}
