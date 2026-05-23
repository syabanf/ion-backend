// Enterprise Phase 2 endpoints — service catalog read + project
// S-Curve. Direct-pgxpool handler in the same delivery style as
// crm/phase2.go and field/phase2.go.
package http

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

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

		// Catalog — readable to anyone with enterprise read perms.
		r.With(httpserver.RequirePermission("enterprise.opportunity.read")).
			Get("/services-catalog", h.listServiceCatalog)

		// S-Curve milestones + computed curve.
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/projects/{id}/milestones", h.listMilestones)
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/projects/{id}/scurve", h.scurve)
		r.With(httpserver.RequirePermission("enterprise.project.manage")).
			Patch("/project-milestones/{id}", h.patchMilestone)

		// Plan revisions (project-level version history).
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/projects/{id}/plan-revisions", h.listPlanRevisions)
		r.With(httpserver.RequirePermission("enterprise.project.manage")).
			Post("/projects/{id}/plan-revisions", h.createPlanRevision)

		// Vendor benchmarking — aggregate vendor_unit_cost per SKU.
		r.With(httpserver.RequirePermission("enterprise.boq.read")).
			Get("/vendor-benchmarks", h.vendorBenchmarks)

		// Vendor onboarding documents.
		r.With(httpserver.RequirePermission("enterprise.vendor_doc.manage")).
			Get("/vendors/{id}/documents", h.listVendorDocuments)
		r.With(httpserver.RequirePermission("enterprise.vendor_doc.manage")).
			Post("/vendors/{id}/documents", h.uploadVendorDocument)
		r.With(httpserver.RequirePermission("enterprise.vendor_doc.manage")).
			Post("/vendor-documents/{id}/verify", h.verifyVendorDocument)

		// Vendor performance scorecard.
		r.With(httpserver.RequirePermission("enterprise.vendor_metric.read")).
			Get("/vendors/{id}/metrics", h.vendorMetrics)
		r.With(httpserver.RequirePermission("enterprise.vendor_metric.read")).
			Get("/vendor-metrics", h.listAllVendorMetrics)

		// Opportunity revenue forecast widget.
		r.With(httpserver.RequirePermission("enterprise.opportunity.read")).
			Get("/opportunities/{id}/forecast", h.opportunityForecast)

		// SLA template binding to a service catalog row.
		r.With(httpserver.RequirePermission("enterprise.opportunity.read")).
			Patch("/services-catalog/{id}/sla", h.bindCatalogSLA)

		// Milestone-based invoicing trigger (manual).
		r.With(httpserver.RequirePermission("enterprise.invoice.create")).
			Post("/projects/{id}/milestones/{mid}/invoice", h.invoiceMilestone)
	})
}

// =============================================================================
// Service catalog
// =============================================================================

func (h *Phase2Handler) listServiceCatalog(w http.ResponseWriter, r *http.Request) {
	cat := r.URL.Query().Get("category")
	q := `
		SELECT id, code, name, category, COALESCE(description,''),
		       delivery_type, unit, base_price, pricing_type,
		       requires_wo, COALESCE(wo_type,''), active,
		       default_sla_template_id
		FROM enterprise.service_catalog
		WHERE active = TRUE
	`
	args := []any{}
	if cat != "" {
		args = append(args, cat)
		q += " AND category = $1"
	}
	q += " ORDER BY category, name"
	rows, err := h.pool.Query(r.Context(), q, args...)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID                   string   `json:"id"`
		Code                 string   `json:"code"`
		Name                 string   `json:"name"`
		Category             string   `json:"category"`
		Description          string   `json:"description,omitempty"`
		DeliveryType         string   `json:"delivery_type"`
		Unit                 string   `json:"unit"`
		BasePrice            float64  `json:"base_price"`
		PricingType          string   `json:"pricing_type"`
		RequiresWO           bool     `json:"requires_wo"`
		WOType               string   `json:"wo_type,omitempty"`
		Active               bool     `json:"active"`
		DefaultSLATemplateID *string  `json:"default_sla_template_id,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var sla *uuid.UUID
		if err := rows.Scan(&x.ID, &x.Code, &x.Name, &x.Category, &x.Description,
			&x.DeliveryType, &x.Unit, &x.BasePrice, &x.PricingType,
			&x.RequiresWO, &x.WOType, &x.Active, &sla); err != nil {
			writeEntErr(w, http.StatusInternalServerError, err)
			return
		}
		if sla != nil {
			s := sla.String()
			x.DefaultSLATemplateID = &s
		}
		out = append(out, x)
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Project milestones + S-Curve
// =============================================================================

func (h *Phase2Handler) listMilestones(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, seq_no, title, COALESCE(description,''),
		       planned_start, planned_end, planned_weight,
		       actual_start, actual_end, progress_pct
		FROM enterprise.project_milestones
		WHERE project_id = $1
		ORDER BY seq_no ASC
	`, id)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID            string     `json:"id"`
		SeqNo         int        `json:"seq_no"`
		Title         string     `json:"title"`
		Description   string     `json:"description,omitempty"`
		PlannedStart  time.Time  `json:"planned_start"`
		PlannedEnd    time.Time  `json:"planned_end"`
		PlannedWeight float64    `json:"planned_weight"`
		ActualStart   *time.Time `json:"actual_start,omitempty"`
		ActualEnd     *time.Time `json:"actual_end,omitempty"`
		ProgressPct   float64    `json:"progress_pct"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.SeqNo, &x.Title, &x.Description,
			&x.PlannedStart, &x.PlannedEnd, &x.PlannedWeight,
			&x.ActualStart, &x.ActualEnd, &x.ProgressPct); err != nil {
			writeEntErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, x)
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"items": out})
}

// scurve computes the planned-vs-actual cumulative completion curve
// in daily buckets across the full project timeline. Planned weight
// of each milestone is distributed linearly over [planned_start,
// planned_end]; actual contribution is `planned_weight * progress_pct/100`,
// distributed linearly between actual_start and (actual_end OR today).
func (h *Phase2Handler) scurve(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT planned_start, planned_end, planned_weight,
		       actual_start, actual_end, progress_pct
		FROM enterprise.project_milestones
		WHERE project_id = $1
	`, id)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	type m struct {
		pStart, pEnd time.Time
		pWeight      float64
		aStart, aEnd *time.Time
		progress     float64
	}
	var ms []m
	var minStart, maxEnd time.Time
	for rows.Next() {
		var x m
		if err := rows.Scan(&x.pStart, &x.pEnd, &x.pWeight, &x.aStart, &x.aEnd, &x.progress); err != nil {
			writeEntErr(w, http.StatusInternalServerError, err)
			return
		}
		ms = append(ms, x)
		if minStart.IsZero() || x.pStart.Before(minStart) {
			minStart = x.pStart
		}
		if x.pEnd.After(maxEnd) {
			maxEnd = x.pEnd
		}
		if x.aStart != nil && (minStart.IsZero() || x.aStart.Before(minStart)) {
			minStart = *x.aStart
		}
	}
	if len(ms) == 0 {
		writeEntJSON(w, http.StatusOK, map[string]any{
			"points": []any{},
			"summary": map[string]any{
				"planned_pct": 0, "actual_pct": 0, "variance_pct": 0,
			},
		})
		return
	}

	// Daily buckets, capped at 120 days to keep the response small.
	days := int(math.Min(120, math.Max(7, maxEnd.Sub(minStart).Hours()/24+1)))
	type point struct {
		Day        string  `json:"day"`
		PlannedPct float64 `json:"planned_pct"`
		ActualPct  float64 `json:"actual_pct"`
	}
	pts := make([]point, days)
	now := time.Now()
	for i := 0; i < days; i++ {
		d := minStart.AddDate(0, 0, i)
		var planned, actual float64
		for _, x := range ms {
			// Planned curve — linear ramp inside [pStart, pEnd].
			if !d.Before(x.pEnd) {
				planned += x.pWeight
			} else if !d.Before(x.pStart) {
				dur := x.pEnd.Sub(x.pStart).Hours() / 24
				if dur > 0 {
					elapsed := d.Sub(x.pStart).Hours() / 24
					planned += x.pWeight * (elapsed / dur)
				}
			}
			// Actual curve — only contributes if the milestone has
			// started. Linear ramp from actual_start to actual_end or
			// to now if still open.
			if x.aStart != nil && !d.Before(*x.aStart) {
				end := now
				if x.aEnd != nil {
					end = *x.aEnd
				}
				if !d.Before(end) {
					actual += x.pWeight * (x.progress / 100.0)
				} else {
					dur := end.Sub(*x.aStart).Hours() / 24
					if dur > 0 {
						elapsed := d.Sub(*x.aStart).Hours() / 24
						actual += x.pWeight * (x.progress / 100.0) * (elapsed / dur)
					}
				}
			}
		}
		pts[i] = point{
			Day:        d.Format("2006-01-02"),
			PlannedPct: planned,
			ActualPct:  actual,
		}
	}
	// Summary as of today.
	var todayP, todayA float64
	for _, p := range pts {
		t, _ := time.Parse("2006-01-02", p.Day)
		if !t.After(now) {
			todayP = p.PlannedPct
			todayA = p.ActualPct
		}
	}
	writeEntJSON(w, http.StatusOK, map[string]any{
		"points": pts,
		"summary": map[string]any{
			"planned_pct":   todayP,
			"actual_pct":    todayA,
			"variance_pct":  todayA - todayP,
		},
	})
}

type patchMilestoneInput struct {
	ProgressPct *float64 `json:"progress_pct,omitempty"`
	ActualStart *string  `json:"actual_start,omitempty"`
	ActualEnd   *string  `json:"actual_end,omitempty"`
}

func (h *Phase2Handler) patchMilestone(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var in patchMilestoneInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE enterprise.project_milestones
		SET progress_pct = COALESCE($1, progress_pct),
		    actual_start = CASE WHEN $2::date IS NULL THEN actual_start ELSE $2::date END,
		    actual_end   = CASE WHEN $3::date IS NULL THEN actual_end   ELSE $3::date END,
		    updated_at = NOW()
		WHERE id = $4
	`, in.ProgressPct, in.ActualStart, in.ActualEnd, id); err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Helpers — namespaced to dodge collisions with the larger
// enterprise/http package.
// =============================================================================

func writeEntJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeEntErr(w http.ResponseWriter, status int, err error) {
	writeEntJSON(w, status, map[string]any{
		"error": map[string]string{"message": err.Error()},
	})
}

// =============================================================================
// Plan revisions — read-only list + write a snapshot
// =============================================================================

func (h *Phase2Handler) listPlanRevisions(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT r.id, r.revision_no, COALESCE(r.reason, ''),
		       r.revised_by, COALESCE(u.full_name, ''),
		       r.created_at, r.snapshot_json
		FROM enterprise.project_plan_revisions r
		LEFT JOIN identity.users u ON u.id = r.revised_by
		WHERE r.project_id = $1
		ORDER BY r.revision_no DESC
	`, id)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID          string    `json:"id"`
		RevisionNo  int       `json:"revision_no"`
		Reason      string    `json:"reason,omitempty"`
		RevisedBy   *string   `json:"revised_by,omitempty"`
		RevisedByName string  `json:"revised_by_name,omitempty"`
		CreatedAt   time.Time `json:"created_at"`
		Snapshot    json.RawMessage `json:"snapshot"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var byID *uuid.UUID
		if err := rows.Scan(&x.ID, &x.RevisionNo, &x.Reason,
			&byID, &x.RevisedByName, &x.CreatedAt, &x.Snapshot); err != nil {
			writeEntErr(w, http.StatusInternalServerError, err)
			return
		}
		if byID != nil {
			s := byID.String()
			x.RevisedBy = &s
		}
		out = append(out, x)
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"items": out})
}

type createRevisionInput struct {
	Reason string `json:"reason,omitempty"`
}

func (h *Phase2Handler) createPlanRevision(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var in createRevisionInput
	_ = json.NewDecoder(r.Body).Decode(&in)

	// Snapshot the current milestone set as JSONB.
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, seq_no, title, planned_start, planned_end,
		       planned_weight, actual_start, actual_end, progress_pct
		FROM enterprise.project_milestones
		WHERE project_id = $1
		ORDER BY seq_no ASC
	`, id)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type mItem struct {
		ID            string     `json:"id"`
		SeqNo         int        `json:"seq_no"`
		Title         string     `json:"title"`
		PlannedStart  time.Time  `json:"planned_start"`
		PlannedEnd    time.Time  `json:"planned_end"`
		PlannedWeight float64    `json:"planned_weight"`
		ActualStart   *time.Time `json:"actual_start,omitempty"`
		ActualEnd     *time.Time `json:"actual_end,omitempty"`
		ProgressPct   float64    `json:"progress_pct"`
	}
	snap := []mItem{}
	for rows.Next() {
		var x mItem
		if err := rows.Scan(&x.ID, &x.SeqNo, &x.Title, &x.PlannedStart, &x.PlannedEnd,
			&x.PlannedWeight, &x.ActualStart, &x.ActualEnd, &x.ProgressPct); err == nil {
			snap = append(snap, x)
		}
	}
	snapBytes, _ := json.Marshal(map[string]any{
		"captured_at": time.Now(),
		"milestones":  snap,
	})

	// Next revision number = max(revision_no)+1, default 1.
	var nextRev int
	_ = h.pool.QueryRow(r.Context(), `
		SELECT COALESCE(MAX(revision_no), 0) + 1
		FROM enterprise.project_plan_revisions WHERE project_id = $1
	`, id).Scan(&nextRev)

	revID := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO enterprise.project_plan_revisions
			(id, project_id, revision_no, snapshot_json, reason, revised_by)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULL)
	`, revID, id, nextRev, snapBytes, in.Reason); err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEntJSON(w, http.StatusCreated, map[string]any{
		"id":          revID.String(),
		"revision_no": nextRev,
	})
}

// =============================================================================
// Vendor benchmarking — per-SKU min/avg/max vendor_unit_cost across
// every BOQ line that has a filled cost. The web "Should we accept
// this vendor quote?" widget reads this to spot outliers.
// =============================================================================

// vendorBenchmarks returns per-SKU vendor cost statistics PLUS — when
// the optional Wave 107 `capability` / `min_rating` / `min_jobs`
// filters are supplied — a per-provider ranking pulled from
// vendor.providers (joined against vendor.provider_capabilities for
// the capability filter). Permission: enterprise.boq.read.
//
// Two modes:
//   - Legacy (no filters or `sku=`): per-SKU stats from boq_lines.
//   - Wave 107 (capability+/min_rating+/min_jobs+): provider ranking
//     ordered by rating × completion.
//
// Both surfaces co-exist so existing clients of the per-SKU view keep
// working unchanged. Vendor schema may not exist in legacy deployments;
// the provider query catches the missing-table error and degrades to
// an empty `providers` list.
func (h *Phase2Handler) vendorBenchmarks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sku := q.Get("sku")
	capability := q.Get("capability")
	minRating := q.Get("min_rating")
	minJobs := q.Get("min_jobs")

	// --- Legacy per-SKU stats ---
	sqlText := `
		SELECT sku,
		       COUNT(*) FILTER (WHERE vendor_unit_cost IS NOT NULL) AS quotes,
		       MIN(vendor_unit_cost),
		       AVG(vendor_unit_cost),
		       MAX(vendor_unit_cost),
		       (PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY vendor_unit_cost))::numeric AS median
		FROM enterprise.boq_lines
		WHERE vendor_unit_cost IS NOT NULL
	`
	args := []any{}
	if sku != "" {
		args = append(args, sku)
		sqlText += " AND sku = $1"
	}
	sqlText += " GROUP BY sku ORDER BY sku"

	rows, err := h.pool.Query(r.Context(), sqlText, args...)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type benchRow struct {
		SKU    string   `json:"sku"`
		Quotes int      `json:"quotes"`
		Min    *float64 `json:"min,omitempty"`
		Avg    *float64 `json:"avg,omitempty"`
		Max    *float64 `json:"max,omitempty"`
		Median *float64 `json:"median,omitempty"`
	}
	out := []benchRow{}
	for rows.Next() {
		var x benchRow
		if err := rows.Scan(&x.SKU, &x.Quotes, &x.Min, &x.Avg, &x.Max, &x.Median); err == nil {
			out = append(out, x)
		}
	}

	// --- Wave 107 provider ranking ---
	providers := []map[string]any{}
	if capability != "" || minRating != "" || minJobs != "" {
		providers = h.queryProviderRanking(r, capability, minRating, minJobs)
	}

	writeEntJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"providers": providers,
	})
}

// queryProviderRanking runs the Wave 107 provider rank query. Returns
// empty on any error (e.g. vendor schema missing) so the legacy
// per-SKU surface keeps responding even in deployments without the
// vendor migration applied.
func (h *Phase2Handler) queryProviderRanking(r *http.Request, capability, minRating, minJobs string) []map[string]any {
	var (
		clauses []string
		args    []any
	)
	clauses = append(clauses, "p.status = 'active'")
	join := ""
	if capability != "" {
		args = append(args, capability)
		join = " JOIN vendor.provider_capabilities c ON c.provider_id = p.id AND c.capability_key = $1"
	}
	if minRating != "" {
		args = append(args, minRating)
		clauses = append(clauses, "p.rating_score >= $"+strconv.Itoa(len(args)))
	}
	if minJobs != "" {
		args = append(args, minJobs)
		clauses = append(clauses, "p.total_completed_jobs >= $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	sql := `
		SELECT p.id, p.name, p.rating_score, p.total_completed_jobs, p.total_revenue
		FROM vendor.providers p` + join + where + `
		ORDER BY p.rating_score DESC, p.total_completed_jobs DESC
		LIMIT 50
	`
	rows, err := h.pool.Query(r.Context(), sql, args...)
	if err != nil {
		// Vendor schema may not be applied yet; degrade silently.
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id      uuid.UUID
			name    string
			rating  float64
			jobs    int
			revenue float64
		)
		if err := rows.Scan(&id, &name, &rating, &jobs, &revenue); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":                   id.String(),
			"name":                 name,
			"rating_score":         rating,
			"total_completed_jobs": jobs,
			"total_revenue":        revenue,
		})
	}
	return out
}
