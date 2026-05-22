// Phase 2 CRM endpoints — bypasses the hexagonal arch deliberately
// (single-file pgxpool handler) to keep delivery quick. The MVP
// surface only needs read+create endpoints; once volume justifies it
// we'll fold these into the regular usecase layer.
package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Phase2Handler exposes the post-activation product surface:
//
//	GET    /addons                            — addon catalog (active only)
//	POST   /customers/{id}/addons             — sell an addon to a customer
//	GET    /customers/{id}/addons             — list addons for a customer
//	POST   /customers/{id}/plan-change        — request plan upgrade / downgrade
//	GET    /customers/{id}/plan-changes       — list plan-change history
//	POST   /customers/{id}/relocation         — start a relocation request
//	GET    /customers/{id}/relocations        — list relocation history
//
// All routes inherit RequireAuth from the surrounding Group. The
// per-route permission is bolted on via chi.With(RequirePermission).
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

		// Catalog
		r.With(httpserver.RequirePermission("crm.addon.read")).
			Get("/addons", h.listAddons)

		// Customer add-ons
		r.With(httpserver.RequirePermission("crm.addon.sell")).
			Post("/customers/{id}/addons", h.sellAddon)
		r.With(httpserver.RequirePermission("crm.addon.read")).
			Get("/customers/{id}/addons", h.listCustomerAddons)

		// Plan changes — submit + queue + decide
		r.With(httpserver.RequirePermission("crm.plan_change.create")).
			Post("/customers/{id}/plan-change", h.requestPlanChange)
		r.With(httpserver.RequirePermission("crm.plan_change.create")).
			Get("/customers/{id}/plan-changes", h.listPlanChanges)
		r.With(httpserver.RequirePermission("crm.plan_change.decide")).
			Get("/plan-changes/pending", h.listPendingPlanChanges)
		r.With(httpserver.RequirePermission("crm.plan_change.decide")).
			Patch("/plan-changes/{id}", h.decidePlanChange)

		// Sales rep self-view — commission read
		r.With(httpserver.RequirePermission("crm.commission.read.own")).
			Get("/commissions/mine", h.myCommissions)
		r.With(httpserver.RequirePermission("crm.commission.read.own")).
			Get("/sales/pipeline-revenue", h.pipelineRevenue)
		r.With(httpserver.RequirePermission("crm.commission.read.own")).
			Get("/sales/my-quota", h.myQuota)
		r.With(httpserver.RequirePermission("crm.commission.read.own")).
			Get("/sales/leaderboard", h.leaderboard)

		// Relocation — submit + queue + survey + decide
		r.With(httpserver.RequirePermission("crm.relocation.create")).
			Post("/customers/{id}/relocation", h.requestRelocation)
		r.With(httpserver.RequirePermission("crm.relocation.create")).
			Get("/customers/{id}/relocations", h.listRelocations)
		r.With(httpserver.RequirePermission("crm.relocation.decide")).
			Get("/relocations/pending", h.listPendingRelocations)
		r.With(httpserver.RequirePermission("crm.relocation.decide")).
			Patch("/relocations/{id}", h.decideRelocation)

		// Lead timeline + overdue lead alert.
		r.With(httpserver.RequirePermission("crm.lead_event.read")).
			Get("/leads/{id}/events", h.listLeadEvents)
		r.With(httpserver.RequirePermission("crm.lead.read")).
			Get("/leads/overdue", h.listOverdueLeads)

		// CS-side referral creation (PRD §6.3 line 1430). CS agent
		// answering an inbound support call can flag the contact as
		// a sales lead with one POST; sales picks it up from the
		// leads board with source='cs_referral'.
		r.With(httpserver.RequirePermission("field.ticket.create")).
			Post("/cs-referrals", h.createCSReferral)
	})
}

// =============================================================================
// CS Referral — CS agent → sales lead
// =============================================================================

type csReferralInput struct {
	FullName   string `json:"full_name"`
	Phone      string `json:"phone"`
	Email      string `json:"email,omitempty"`
	Address    string `json:"address"`
	TicketID   string `json:"ticket_id,omitempty"` // referring ticket, optional
	Notes      string `json:"notes,omitempty"`
}

func (h *Phase2Handler) createCSReferral(w http.ResponseWriter, r *http.Request) {
	var in csReferralInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.FullName == "" || in.Phone == "" || in.Address == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"full_name, phone, address are required"})
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var createdBy *uuid.UUID
	if claims != nil {
		u := claims.UserID
		createdBy = &u
	}
	id := uuid.New()
	leadNo := fmt.Sprintf("LD-%s-%s", time.Now().Format("20060102"), id.String()[:8])
	notes := in.Notes
	if in.TicketID != "" {
		notes = fmt.Sprintf("Referred from ticket %s. %s", in.TicketID, notes)
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO crm.leads (
			id, lead_number, full_name, phone, email, address,
			status, source, notes, created_by
		) VALUES (
			$1, $2, $3, $4, NULLIF($5,''), $6,
			'new', 'cs_referral', NULLIF($7,''), $8
		)
	`, id, leadNo, in.FullName, in.Phone, in.Email, in.Address,
		notes, createdBy); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Drop the first timeline event manually since we bypass
	// updateLead's auto-write path.
	_, _ = h.pool.Exec(r.Context(), `
		INSERT INTO crm.lead_events (lead_id, actor_user_id, kind, summary, data)
		VALUES ($1, $2, 'created', $3, $4::jsonb)
	`, id, createdBy, "Lead created from CS referral",
		fmt.Sprintf(`{"source":"cs_referral","ticket_id":"%s"}`, in.TicketID))
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          id.String(),
		"lead_number": leadNo,
		"status":      "new",
		"source":      "cs_referral",
	})
}

// =============================================================================
// Lead events — timeline
// =============================================================================

func (h *Phase2Handler) listLeadEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT e.id, e.kind, e.summary, e.data, e.created_at,
		       e.actor_user_id, COALESCE(u.full_name, u.email, '')
		FROM crm.lead_events e
		LEFT JOIN identity.users u ON u.id = e.actor_user_id
		WHERE e.lead_id = $1
		ORDER BY e.created_at DESC
		LIMIT 200
	`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID        string          `json:"id"`
		Kind      string          `json:"kind"`
		Summary   string          `json:"summary"`
		Data      json.RawMessage `json:"data,omitempty"`
		CreatedAt time.Time       `json:"created_at"`
		ActorID   *string         `json:"actor_user_id,omitempty"`
		ActorName string          `json:"actor_name,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var actorID *uuid.UUID
		if err := rows.Scan(&x.ID, &x.Kind, &x.Summary, &x.Data, &x.CreatedAt,
			&actorID, &x.ActorName); err == nil {
			if actorID != nil {
				s := actorID.String()
				x.ActorID = &s
			}
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// listOverdueLeads — "no activity since N days" alert source.
//
// Wave 76 (TC-CRM-014): the default threshold is now read from
// identity.platform_config (key=lead_overdue_days, seeded by 0049),
// so admins can change it without a deploy. The ?days= query param
// still overrides per-request for ad-hoc reporting.
func (h *Phase2Handler) listOverdueLeads(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no claims"})
		return
	}
	mine := r.URL.Query().Get("mine") == "true"
	daysStr := r.URL.Query().Get("days")
	// Default falls back to the platform_config value, then to 7 if
	// the row is missing (e.g. pre-migration environments).
	days := 7
	var cfgValue string
	if err := h.pool.QueryRow(r.Context(),
		`SELECT config_value FROM identity.platform_config WHERE config_key = 'lead_overdue_days'`,
	).Scan(&cfgValue); err == nil {
		if d, perr := strconv.Atoi(cfgValue); perr == nil && d > 0 && d < 365 {
			days = d
		}
	}
	if d, err := strconv.Atoi(daysStr); err == nil && d > 0 && d < 365 {
		days = d
	}
	q := `
		SELECT id, lead_number, full_name, phone, status, source,
		       updated_at, sales_id
		FROM crm.leads
		WHERE updated_at < NOW() - make_interval(days => $1)
		  AND status NOT IN ('converted','lost','rejected')
	`
	args := []any{days}
	if mine {
		args = append(args, claims.UserID)
		q += " AND sales_id = $2"
	}
	q += " ORDER BY updated_at ASC LIMIT 200"
	rows, err := h.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID         string    `json:"id"`
		LeadNumber string    `json:"lead_number"`
		FullName   string    `json:"full_name"`
		Phone      string    `json:"phone"`
		Status     string    `json:"status"`
		Source     string    `json:"source"`
		UpdatedAt  time.Time `json:"updated_at"`
		SalesID    *string   `json:"sales_id,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var sid *uuid.UUID
		if err := rows.Scan(&x.ID, &x.LeadNumber, &x.FullName, &x.Phone,
			&x.Status, &x.Source, &x.UpdatedAt, &sid); err == nil {
			if sid != nil {
				s := sid.String()
				x.SalesID = &s
			}
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"threshold_days": days,
	})
}

// =============================================================================
// Add-on catalog
// =============================================================================

type addonDTO struct {
	ID              string  `json:"id"`
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	AddonType       string  `json:"addon_type"`
	OneTimeFee      float64 `json:"one_time_fee"`
	MonthlyFee      float64 `json:"monthly_fee"`
	RequiresInstall bool    `json:"requires_install"`
	Active          bool    `json:"active"`
	Description     string  `json:"description,omitempty"`
}

func (h *Phase2Handler) listAddons(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, code, name, addon_type, one_time_fee, monthly_fee,
		       requires_install, active, COALESCE(description,'')
		FROM crm.product_addons
		WHERE active = TRUE
		ORDER BY addon_type, name
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []addonDTO{}
	for rows.Next() {
		var a addonDTO
		if err := rows.Scan(&a.ID, &a.Code, &a.Name, &a.AddonType, &a.OneTimeFee, &a.MonthlyFee,
			&a.RequiresInstall, &a.Active, &a.Description); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Customer add-ons — sell + list
// =============================================================================

type sellAddonInput struct {
	AddonID  string `json:"addon_id"`
	Quantity int    `json:"quantity"`
	Notes    string `json:"notes,omitempty"`
}

type customerAddonDTO struct {
	ID          string    `json:"id"`
	CustomerID  string    `json:"customer_id"`
	AddonID     string    `json:"addon_id"`
	AddonCode   string    `json:"addon_code"`
	AddonName   string    `json:"addon_name"`
	AddonType   string    `json:"addon_type"`
	Status      string    `json:"status"`
	Quantity    int       `json:"quantity"`
	OneTimeFee  float64   `json:"one_time_fee"`
	MonthlyFee  float64   `json:"monthly_fee"`
	InstallWoID *string   `json:"install_wo_id,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

func (h *Phase2Handler) sellAddon(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in sellAddonInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	addonID, err := uuid.Parse(in.AddonID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var salesRepID *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		salesRepID = &uid
	}

	// Snapshot the catalog prices at sell-time so historical reports
	// don't shift when the catalog changes.
	var (
		oneTime, monthly float64
		requiresInstall  bool
		addonCode        string
		addonName        string
		addonType        string
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT one_time_fee, monthly_fee, requires_install, code, name, addon_type
		FROM crm.product_addons
		WHERE id = $1 AND active = TRUE
	`, addonID).Scan(&oneTime, &monthly, &requiresInstall, &addonCode, &addonName, &addonType); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusBadRequest, errMsg{"addon not found or inactive"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	id := uuid.New()
	status := "active"
	if requiresInstall {
		status = "pending_install"
	}

	// Capture the customer's address so we can pre-fill the install WO
	// when one is needed. Failure here isn't fatal — we still record the
	// addon sale; the WO just won't have an address column populated.
	var customerAddress string
	_ = h.pool.QueryRow(r.Context(), `
		SELECT COALESCE(address,'') FROM crm.customers WHERE id = $1
	`, customerID).Scan(&customerAddress)

	// Open a tx so the addon insert + the install-WO insert + the
	// addon→WO link land atomically. Cross-schema (`crm.*` + `field.*`)
	// is safe — same DB, single connection.
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO crm.customer_addons (
			id, customer_id, addon_id, sales_rep_id, status, quantity,
			one_time_fee, monthly_fee, notes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''))
	`, id, customerID, addonID, salesRepID, status, in.Quantity,
		oneTime*float64(in.Quantity), monthly*float64(in.Quantity), in.Notes); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Auto-create the install WO when the addon needs a tech visit.
	// We use `wo_type='new_installation'` because we don't yet have an
	// `addon_install` sub-type — the field workflow handles it the
	// same way as a fresh install, and the linkage back to the addon
	// row is by FK on crm.customer_addons.install_wo_id.
	var installWoID *uuid.UUID
	if requiresInstall {
		woID := uuid.New()
		woNumber := fmt.Sprintf("WO-%s-%s", time.Now().Format("20060102"), woID.String()[:8])
		if _, err := tx.Exec(ctx, `
			INSERT INTO field.work_orders (
				id, wo_number, customer_id, wo_type, product_type, address,
				status, priority
			) VALUES ($1, $2, $3, 'new_installation', 'addon', $4, 'unassigned', 'medium')
		`, woID, woNumber, customerID,
			fallback(customerAddress, "(address unknown)")); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := tx.Exec(ctx, `
			UPDATE crm.customer_addons SET install_wo_id = $1 WHERE id = $2
		`, woID, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		installWoID = &woID
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	resp := map[string]any{
		"id":               id.String(),
		"customer_id":      customerID.String(),
		"addon_id":         addonID.String(),
		"addon_code":       addonCode,
		"addon_name":       addonName,
		"addon_type":       addonType,
		"status":           status,
		"quantity":         in.Quantity,
		"one_time_fee":     oneTime * float64(in.Quantity),
		"monthly_fee":      monthly * float64(in.Quantity),
		"requires_install": requiresInstall,
	}
	if installWoID != nil {
		resp["install_wo_id"] = installWoID.String()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func fallback(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

func (h *Phase2Handler) listCustomerAddons(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT ca.id, ca.customer_id, ca.addon_id, pa.code, pa.name, pa.addon_type,
		       ca.status, ca.quantity, ca.one_time_fee, ca.monthly_fee,
		       ca.install_wo_id, COALESCE(ca.notes,''), ca.requested_at, ca.activated_at
		FROM crm.customer_addons ca
		JOIN crm.product_addons pa ON pa.id = ca.addon_id
		WHERE ca.customer_id = $1
		ORDER BY ca.requested_at DESC
	`, customerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []customerAddonDTO{}
	for rows.Next() {
		var a customerAddonDTO
		var installWO *uuid.UUID
		var act *time.Time
		if err := rows.Scan(&a.ID, &a.CustomerID, &a.AddonID, &a.AddonCode, &a.AddonName,
			&a.AddonType, &a.Status, &a.Quantity, &a.OneTimeFee, &a.MonthlyFee,
			&installWO, &a.Notes, &a.RequestedAt, &act); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if installWO != nil {
			s := installWO.String()
			a.InstallWoID = &s
		}
		a.ActivatedAt = act
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Plan changes
// =============================================================================

type planChangeInput struct {
	ToProductID  string `json:"to_product_id"`
	ChangeKind   string `json:"change_kind"`
	Reason       string `json:"reason,omitempty"`
	EffectiveAt  string `json:"effective_at,omitempty"` // RFC3339; omitempty → defaults next cycle
}

func (h *Phase2Handler) requestPlanChange(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in planChangeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	toProductID, err := uuid.Parse(in.ToProductID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.ChangeKind != "upgrade" && in.ChangeKind != "downgrade" {
		writeErr(w, http.StatusBadRequest, errMsg{"change_kind must be upgrade or downgrade"})
		return
	}
	// Resolve the customer's current product so we can snapshot it.
	var fromProductID uuid.UUID
	if err := h.pool.QueryRow(r.Context(), `
		SELECT product_id FROM crm.orders WHERE customer_id = $1
		ORDER BY created_at DESC LIMIT 1
	`, customerID).Scan(&fromProductID); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusBadRequest, errMsg{"customer has no active order; can't infer current plan"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var effectiveAt *time.Time
	if in.EffectiveAt != "" {
		t, err := time.Parse(time.RFC3339, in.EffectiveAt)
		if err == nil {
			effectiveAt = &t
		}
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var salesRepID *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		salesRepID = &uid
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO crm.plan_change_requests (
			id, customer_id, from_product_id, to_product_id, change_kind,
			reason, effective_at, sales_rep_id, status
		) VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7, $8, 'pending')
	`, id, customerID, fromProductID, toProductID, in.ChangeKind, in.Reason,
		effectiveAt, salesRepID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           id.String(),
		"status":       "pending",
		"change_kind":  in.ChangeKind,
		"to_product":   toProductID.String(),
		"from_product": fromProductID.String(),
	})
}

func (h *Phase2Handler) listPlanChanges(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT pc.id, pc.customer_id,
		       fp.code AS from_code, fp.name AS from_name,
		       tp.code AS to_code,   tp.name AS to_name,
		       pc.change_kind, COALESCE(pc.reason,''), pc.status,
		       pc.effective_at, pc.applied_at, pc.created_at
		FROM crm.plan_change_requests pc
		LEFT JOIN crm.products fp ON fp.id = pc.from_product_id
		LEFT JOIN crm.products tp ON tp.id = pc.to_product_id
		WHERE pc.customer_id = $1
		ORDER BY pc.created_at DESC
	`, customerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID          string     `json:"id"`
		CustomerID  string     `json:"customer_id"`
		FromCode    string     `json:"from_code"`
		FromName    string     `json:"from_name"`
		ToCode      string     `json:"to_code"`
		ToName      string     `json:"to_name"`
		ChangeKind  string     `json:"change_kind"`
		Reason      string     `json:"reason,omitempty"`
		Status      string     `json:"status"`
		EffectiveAt *time.Time `json:"effective_at,omitempty"`
		AppliedAt   *time.Time `json:"applied_at,omitempty"`
		CreatedAt   time.Time  `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.CustomerID, &x.FromCode, &x.FromName,
			&x.ToCode, &x.ToName, &x.ChangeKind, &x.Reason, &x.Status,
			&x.EffectiveAt, &x.AppliedAt, &x.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Relocation
// =============================================================================

type relocationInput struct {
	ToAddress  string   `json:"to_address"`
	ToGPSLat   *float64 `json:"to_gps_lat,omitempty"`
	ToGPSLng   *float64 `json:"to_gps_lng,omitempty"`
	Notes      string   `json:"notes,omitempty"`
}

func (h *Phase2Handler) requestRelocation(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in relocationInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.ToAddress == "" {
		writeErr(w, http.StatusBadRequest, errMsg{"to_address is required"})
		return
	}
	// Snapshot current customer address.
	var fromAddress string
	if err := h.pool.QueryRow(r.Context(), `
		SELECT COALESCE(address,'') FROM crm.customers WHERE id = $1
	`, customerID).Scan(&fromAddress); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusNotFound, errMsg{"customer not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var salesRepID *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		salesRepID = &uid
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO crm.customer_relocations (
			id, customer_id, from_address, to_address,
			to_gps_lat, to_gps_lng, sales_rep_id, status, survey_note
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending_survey', NULLIF($8,''))
	`, id, customerID, fromAddress, in.ToAddress, in.ToGPSLat, in.ToGPSLng,
		salesRepID, in.Notes); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           id.String(),
		"customer_id":  customerID.String(),
		"from_address": fromAddress,
		"to_address":   in.ToAddress,
		"status":       "pending_survey",
	})
}

func (h *Phase2Handler) listRelocations(w http.ResponseWriter, r *http.Request) {
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, customer_id, from_address, to_address, status,
		       requested_at, surveyed_at, completed_at
		FROM crm.customer_relocations
		WHERE customer_id = $1
		ORDER BY requested_at DESC
	`, customerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID           string     `json:"id"`
		CustomerID   string     `json:"customer_id"`
		FromAddress  string     `json:"from_address"`
		ToAddress    string     `json:"to_address"`
		Status       string     `json:"status"`
		RequestedAt  time.Time  `json:"requested_at"`
		SurveyedAt   *time.Time `json:"surveyed_at,omitempty"`
		CompletedAt  *time.Time `json:"completed_at,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.CustomerID, &x.FromAddress, &x.ToAddress,
			&x.Status, &x.RequestedAt, &x.SurveyedAt, &x.CompletedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Plan-change approval queue + decide
// =============================================================================

func (h *Phase2Handler) listPendingPlanChanges(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT pc.id, pc.customer_id, c.full_name,
		       fp.code AS from_code, fp.name AS from_name,
		       tp.code AS to_code,   tp.name AS to_name,
		       pc.change_kind, COALESCE(pc.reason,''), pc.status,
		       pc.effective_at, pc.created_at
		FROM crm.plan_change_requests pc
		LEFT JOIN crm.customers c ON c.id = pc.customer_id
		LEFT JOIN crm.products fp ON fp.id = pc.from_product_id
		LEFT JOIN crm.products tp ON tp.id = pc.to_product_id
		WHERE pc.status = 'pending'
		ORDER BY pc.created_at ASC
		LIMIT 200
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID           string     `json:"id"`
		CustomerID   string     `json:"customer_id"`
		CustomerName string     `json:"customer_name"`
		FromCode     string     `json:"from_code"`
		FromName     string     `json:"from_name"`
		ToCode       string     `json:"to_code"`
		ToName       string     `json:"to_name"`
		ChangeKind   string     `json:"change_kind"`
		Reason       string     `json:"reason,omitempty"`
		Status       string     `json:"status"`
		EffectiveAt  *time.Time `json:"effective_at,omitempty"`
		CreatedAt    time.Time  `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.CustomerID, &x.CustomerName, &x.FromCode, &x.FromName,
			&x.ToCode, &x.ToName, &x.ChangeKind, &x.Reason, &x.Status, &x.EffectiveAt,
			&x.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type decidePlanChangeInput struct {
	Decision string `json:"decision"`     // 'approved' | 'rejected' | 'applied' | 'cancelled'
	Note     string `json:"note,omitempty"`
}

func (h *Phase2Handler) decidePlanChange(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in decidePlanChangeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	switch in.Decision {
	case "approved", "rejected", "applied", "cancelled":
		// ok
	default:
		writeErr(w, http.StatusBadRequest, errMsg{"decision must be one of approved/rejected/applied/cancelled"})
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var decidedBy *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		decidedBy = &uid
	}
	// `applied` is the terminal happy path — set applied_at as well.
	q := `
		UPDATE crm.plan_change_requests
		SET status = $1,
		    decided_by = $2,
		    decision_note = NULLIF($3,''),
		    updated_at = NOW()
	`
	if in.Decision == "applied" {
		q += ", applied_at = NOW()"
	}
	q += " WHERE id = $4 AND status = 'pending'"
	tag, err := h.pool.Exec(r.Context(), q, in.Decision, decidedBy, in.Note, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusConflict, errMsg{"already decided or not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": in.Decision})
}

// =============================================================================
// Relocation survey + decide
// =============================================================================

func (h *Phase2Handler) listPendingRelocations(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT cr.id, cr.customer_id, c.full_name,
		       cr.from_address, cr.to_address, cr.status,
		       cr.requested_at, cr.surveyed_at
		FROM crm.customer_relocations cr
		LEFT JOIN crm.customers c ON c.id = cr.customer_id
		WHERE cr.status IN ('pending_survey','approved','install_wo_open')
		ORDER BY cr.requested_at ASC
		LIMIT 200
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID           string     `json:"id"`
		CustomerID   string     `json:"customer_id"`
		CustomerName string     `json:"customer_name"`
		FromAddress  string     `json:"from_address"`
		ToAddress    string     `json:"to_address"`
		Status       string     `json:"status"`
		RequestedAt  time.Time  `json:"requested_at"`
		SurveyedAt   *time.Time `json:"surveyed_at,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.CustomerID, &x.CustomerName, &x.FromAddress,
			&x.ToAddress, &x.Status, &x.RequestedAt, &x.SurveyedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type decideRelocationInput struct {
	Decision   string `json:"decision"`            // 'approved' | 'rejected' | 'survey_failed' | 'completed' | 'cancelled'
	SurveyNote string `json:"survey_note,omitempty"`
}

func (h *Phase2Handler) decideRelocation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var in decideRelocationInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	switch in.Decision {
	case "approved", "rejected", "survey_failed", "completed", "cancelled":
		// ok
	default:
		writeErr(w, http.StatusBadRequest, errMsg{"decision must be one of approved/rejected/survey_failed/completed/cancelled"})
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var decidedBy *uuid.UUID
	if claims != nil {
		uid := claims.UserID
		decidedBy = &uid
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback(ctx)

	// Survey-time stamping. survey_failed + approved both flip surveyed_at.
	q := `
		UPDATE crm.customer_relocations
		SET status = $1,
		    decided_by = $2,
		    survey_note = COALESCE(NULLIF($3,''), survey_note),
		    updated_at = NOW()
	`
	switch in.Decision {
	case "approved", "survey_failed":
		q += ", surveyed_at = NOW()"
	case "completed":
		q += ", completed_at = NOW()"
	case "cancelled":
		q += ", cancelled_at = NOW()"
	}
	q += " WHERE id = $4 RETURNING customer_id, to_address"
	var customerID uuid.UUID
	var toAddress string
	if err := tx.QueryRow(ctx, q, in.Decision, decidedBy, in.SurveyNote, id).
		Scan(&customerID, &toAddress); err != nil {
		if err == pgx.ErrNoRows {
			writeErr(w, http.StatusNotFound, errMsg{"relocation not found"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// On approval, also open the install WO at the new address (and
	// flip relocation status to install_wo_open). The decision flow is
	// one-step today: approved → install_wo_open in the same hop.
	var installWoID *uuid.UUID
	if in.Decision == "approved" {
		woID := uuid.New()
		woNumber := fmt.Sprintf("WO-%s-%s", time.Now().Format("20060102"), woID.String()[:8])
		if _, err := tx.Exec(ctx, `
			INSERT INTO field.work_orders (
				id, wo_number, customer_id, wo_type, product_type, address,
				status, priority
			) VALUES ($1, $2, $3, 'new_installation', 'broadband', $4, 'unassigned', 'medium')
		`, woID, woNumber, customerID, toAddress); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := tx.Exec(ctx, `
			UPDATE crm.customer_relocations
			SET status = 'install_wo_open', install_wo_id = $1
			WHERE id = $2
		`, woID, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		installWoID = &woID
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{"ok": true, "status": in.Decision}
	if installWoID != nil {
		resp["install_wo_id"] = installWoID.String()
		resp["status"] = "install_wo_open"
	}
	writeJSON(w, http.StatusOK, resp)
}

// =============================================================================
// Sales-rep self-view — commission + pipeline revenue
// =============================================================================

func (h *Phase2Handler) myCommissions(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT cr.id, cr.order_id, cr.party_type, cr.amount, cr.percentage,
		       cr.base_amount, COALESCE(cr.notes,''), cr.created_at,
		       c.full_name AS customer_name
		FROM billing.commission_records cr
		LEFT JOIN crm.customers c ON c.id = cr.customer_id
		WHERE cr.user_id = $1
		ORDER BY cr.created_at DESC
		LIMIT 200
	`, claims.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID           string    `json:"id"`
		OrderID      string    `json:"order_id"`
		PartyType    string    `json:"party_type"`
		Amount       float64   `json:"amount"`
		Percentage   float64   `json:"percentage"`
		BaseAmount   float64   `json:"base_amount"`
		Notes        string    `json:"notes,omitempty"`
		CreatedAt    time.Time `json:"created_at"`
		CustomerName string    `json:"customer_name"`
	}
	out := []row{}
	var totalEarned float64
	var totalThisMonth float64
	nowMonth := time.Now().Format("2006-01")
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.OrderID, &x.PartyType, &x.Amount,
			&x.Percentage, &x.BaseAmount, &x.Notes, &x.CreatedAt, &x.CustomerName); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
		totalEarned += x.Amount
		if x.CreatedAt.Format("2006-01") == nowMonth {
			totalThisMonth += x.Amount
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":            out,
		"total_earned":     totalEarned,
		"total_this_month": totalThisMonth,
		"count":            len(out),
	})
}

// pipelineRevenue — aggregate revenue forecast for the rep's leads.
// Sums the monthly_price of products associated with each lead's
// chosen product (or zero when no product picked yet) × 12 to give a
// year-1 estimate. Crude but usable for the Home tab Stats card.
func (h *Phase2Handler) pipelineRevenue(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var (
		pipeMonthly  float64
		pipeOtc      float64
		leadsScored  int
		convThisMonth int
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT
		  COALESCE(SUM(p.monthly_price), 0),
		  COALESCE(SUM(p.otc_price), 0),
		  COUNT(*) FILTER (WHERE l.product_id IS NOT NULL),
		  COUNT(*) FILTER (WHERE l.status = 'converted'
		                   AND date_trunc('month', l.created_at) = date_trunc('month', NOW()))
		FROM crm.leads l
		LEFT JOIN crm.products p ON p.id = l.product_id
		WHERE l.sales_id = $1
		  AND l.status NOT IN ('lost')
	`, claims.UserID).Scan(&pipeMonthly, &pipeOtc, &leadsScored, &convThisMonth); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	year1 := pipeMonthly*12 + pipeOtc
	writeJSON(w, http.StatusOK, map[string]any{
		"pipeline_monthly":    pipeMonthly,
		"pipeline_otc":        pipeOtc,
		"pipeline_year1_est":  year1,
		"leads_with_product":  leadsScored,
		"converted_this_month": convThisMonth,
	})
}

// =============================================================================
// Sales — Quota + Leaderboard (manager view)
// =============================================================================

func (h *Phase2Handler) myQuota(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeErr(w, http.StatusUnauthorized, errMsg{"no session"})
		return
	}
	var (
		targetOrders  int
		targetRevenue float64
		actualOrders  int
		actualRevenue float64
	)
	// Current month period.
	monthKey := time.Now().Format("2006-01-02") // gets coerced; only year+month matter
	_ = h.pool.QueryRow(r.Context(), `
		SELECT target_orders, target_revenue
		FROM crm.sales_quotas
		WHERE user_id = $1 AND period_month = date_trunc('month', $2::timestamp)::date
	`, claims.UserID, monthKey).Scan(&targetOrders, &targetRevenue)

	_ = h.pool.QueryRow(r.Context(), `
		SELECT COUNT(*), COALESCE(SUM(p.monthly_price),0) + COALESCE(SUM(p.otc_price),0)
		FROM crm.leads l
		LEFT JOIN crm.products p ON p.id = l.product_id
		WHERE l.sales_id = $1
		  AND l.status = 'converted'
		  AND date_trunc('month', l.created_at) = date_trunc('month', NOW())
	`, claims.UserID).Scan(&actualOrders, &actualRevenue)

	writeJSON(w, http.StatusOK, map[string]any{
		"period":         time.Now().Format("January 2006"),
		"target_orders":  targetOrders,
		"target_revenue": targetRevenue,
		"actual_orders":  actualOrders,
		"actual_revenue": actualRevenue,
	})
}

func (h *Phase2Handler) leaderboard(w http.ResponseWriter, r *http.Request) {
	// Aggregates this-month conversions per sales rep + ranks. Anyone
	// with crm.commission.read.own can see the leaderboard (everyone
	// sees the same ranking — PRD §6.1 says "Top performing reps").
	rows, err := h.pool.Query(r.Context(), `
		SELECT
		  l.sales_id,
		  u.full_name,
		  COUNT(*) FILTER (WHERE l.status = 'converted')                                 AS conv_count,
		  COUNT(*)                                                                        AS leads_total,
		  COALESCE(SUM(p.monthly_price) FILTER (WHERE l.status = 'converted'), 0)         AS conv_revenue
		FROM crm.leads l
		LEFT JOIN identity.users u ON u.id = l.sales_id
		LEFT JOIN crm.products p ON p.id = l.product_id
		WHERE l.sales_id IS NOT NULL
		  AND date_trunc('month', l.created_at) = date_trunc('month', NOW())
		GROUP BY l.sales_id, u.full_name
		ORDER BY conv_count DESC, conv_revenue DESC
		LIMIT 50
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		UserID      string  `json:"user_id"`
		FullName    string  `json:"full_name"`
		Conversions int     `json:"conversions"`
		LeadsTotal  int     `json:"leads_total"`
		Revenue     float64 `json:"revenue"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.UserID, &x.FullName, &x.Conversions, &x.LeadsTotal, &x.Revenue); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Small helpers — JSON write + tiny error wrapper.
// =============================================================================

type errMsg struct{ Message string }

func (e errMsg) Error() string { return e.Message }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"message": err.Error()},
	})
}
