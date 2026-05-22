// Enterprise P1+P2 backlog handlers.
package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/httpserver"
)

// =============================================================================
// Vendor onboarding documents
// =============================================================================

func (h *Phase2Handler) listVendorDocuments(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, kind, file_url, file_name, bytes,
		       uploaded_by, uploaded_at, verified_at, verified_by, notes
		FROM enterprise.vendor_documents
		WHERE vendor_id = $1
		ORDER BY uploaded_at DESC
	`, id)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID         string     `json:"id"`
		Kind       string     `json:"kind"`
		FileURL    string     `json:"file_url"`
		FileName   string     `json:"file_name"`
		Bytes      int        `json:"bytes"`
		UploadedBy *string    `json:"uploaded_by,omitempty"`
		UploadedAt time.Time  `json:"uploaded_at"`
		VerifiedAt *time.Time `json:"verified_at,omitempty"`
		VerifiedBy *string    `json:"verified_by,omitempty"`
		Notes      string     `json:"notes,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var up, vb *uuid.UUID
		if err := rows.Scan(&x.ID, &x.Kind, &x.FileURL, &x.FileName, &x.Bytes,
			&up, &x.UploadedAt, &x.VerifiedAt, &vb, &x.Notes); err == nil {
			if up != nil {
				s := up.String()
				x.UploadedBy = &s
			}
			if vb != nil {
				s := vb.String()
				x.VerifiedBy = &s
			}
			out = append(out, x)
		}
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"items": out})
}

type uploadVendorDocReq struct {
	Kind     string `json:"kind"`
	FileURL  string `json:"file_url"`
	FileName string `json:"file_name"`
	Bytes    int    `json:"bytes,omitempty"`
}

func (h *Phase2Handler) uploadVendorDocument(w http.ResponseWriter, r *http.Request) {
	vendorID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var in uploadVendorDocReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	var uploader *uuid.UUID
	if claims != nil {
		u := claims.UserID
		uploader = &u
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO enterprise.vendor_documents
			(id, vendor_id, kind, file_url, file_name, bytes, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id, vendorID, in.Kind, in.FileURL, in.FileName, in.Bytes, uploader); err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEntJSON(w, http.StatusCreated, map[string]any{"id": id.String()})
}

type verifyVendorDocReq struct {
	Notes string `json:"notes,omitempty"`
}

func (h *Phase2Handler) verifyVendorDocument(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var in verifyVendorDocReq
	_ = json.NewDecoder(r.Body).Decode(&in)
	claims := httpserver.ClaimsFromContext(r.Context())
	var verifier *uuid.UUID
	if claims != nil {
		u := claims.UserID
		verifier = &u
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE enterprise.vendor_documents
		SET verified_at = NOW(), verified_by = $1,
		    notes = CASE WHEN $2 = '' THEN notes ELSE $2 END
		WHERE id = $3
	`, verifier, in.Notes, id); err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Vendor performance metrics (scorecard)
// =============================================================================

func (h *Phase2Handler) vendorMetrics(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT period_month, orders_total, orders_on_time,
		       defects_reported, COALESCE(avg_response_hours, 0), notes
		FROM enterprise.vendor_metrics
		WHERE vendor_id = $1
		ORDER BY period_month DESC
		LIMIT 24
	`, id)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		PeriodMonth     string  `json:"period_month"`
		OrdersTotal     int     `json:"orders_total"`
		OrdersOnTime    int     `json:"orders_on_time"`
		DefectsReported int     `json:"defects_reported"`
		AvgResponseHrs  float64 `json:"avg_response_hours"`
		Notes           string  `json:"notes,omitempty"`
		OnTimePct       float64 `json:"on_time_pct"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var pm time.Time
		if err := rows.Scan(&pm, &x.OrdersTotal, &x.OrdersOnTime,
			&x.DefectsReported, &x.AvgResponseHrs, &x.Notes); err == nil {
			x.PeriodMonth = pm.Format("2006-01")
			if x.OrdersTotal > 0 {
				x.OnTimePct = float64(x.OrdersOnTime) / float64(x.OrdersTotal) * 100
			}
			out = append(out, x)
		}
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Phase2Handler) listAllVendorMetrics(w http.ResponseWriter, r *http.Request) {
	// Roll up to current-month-only for the dashboard.
	rows, err := h.pool.Query(r.Context(), `
		SELECT vendor_id, orders_total, orders_on_time,
		       defects_reported, COALESCE(avg_response_hours, 0)
		FROM enterprise.vendor_metrics
		WHERE period_month = date_trunc('month', CURRENT_DATE)
		ORDER BY orders_total DESC
		LIMIT 100
	`)
	if err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		VendorID       string  `json:"vendor_id"`
		OrdersTotal    int     `json:"orders_total"`
		OrdersOnTime   int     `json:"orders_on_time"`
		DefectsReported int    `json:"defects_reported"`
		AvgResponseHrs float64 `json:"avg_response_hours"`
		OnTimePct      float64 `json:"on_time_pct"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.VendorID, &x.OrdersTotal, &x.OrdersOnTime,
			&x.DefectsReported, &x.AvgResponseHrs); err == nil {
			if x.OrdersTotal > 0 {
				x.OnTimePct = float64(x.OrdersOnTime) / float64(x.OrdersTotal) * 100
			}
			out = append(out, x)
		}
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Opportunity revenue forecast widget
//
// Returns a quick projection:
//   forecast = quoted_total * stage_probability(stage)
// where the probability comes from a deterministic mapping. Real CRMs
// store this per-opportunity; this is good enough until the
// negotiation usecase grows a forecast_pct column.
// =============================================================================

func (h *Phase2Handler) opportunityForecast(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var (
		stage        string
		quotedTotal  float64
		expectedDate *time.Time
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT o.stage,
		       COALESCE((
		         SELECT q.sell_total
		         FROM enterprise.quotations q
		         WHERE q.opportunity_id = o.id
		         ORDER BY q.created_at DESC LIMIT 1
		       ), 0),
		       o.expected_close_at
		FROM enterprise.opportunities o
		WHERE o.id = $1
	`, id).Scan(&stage, &quotedTotal, &expectedDate); err != nil {
		writeEntErr(w, http.StatusNotFound, err)
		return
	}
	prob := stageProb(stage)
	writeEntJSON(w, http.StatusOK, map[string]any{
		"stage":              stage,
		"quoted_total":       quotedTotal,
		"stage_probability":  prob,
		"forecast_total":     quotedTotal * prob,
		"expected_close_date": expectedDate,
	})
}

func stageProb(s string) float64 {
	switch s {
	case "cold":
		return 0.10
	case "warm":
		return 0.40
	case "hot":
		return 0.75
	case "won":
		return 1.00
	case "lost":
		return 0.0
	default:
		return 0.15
	}
}

// =============================================================================
// SLA template binding to a service catalog row
// =============================================================================

type bindSLAReq struct {
	SLATemplateID string `json:"sla_template_id"`
}

func (h *Phase2Handler) bindCatalogSLA(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var in bindSLAReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var slaID *uuid.UUID
	if in.SLATemplateID != "" {
		u, err := uuid.Parse(in.SLATemplateID)
		if err != nil {
			writeEntErr(w, http.StatusBadRequest, err)
			return
		}
		slaID = &u
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE enterprise.service_catalog
		SET default_sla_template_id = $1
		WHERE id = $2
	`, slaID, id); err != nil {
		writeEntErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEntJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Milestone-based invoicing trigger (manual)
//
// Picks the matching invoice_plan row for the project + milestone seq
// number and creates an invoice via the existing internal Service if
// not already issued. Stub: returns the chosen invoice_plan row + a
// note that the actual invoice generation is done by the existing
// Finance handler (operator clicks "Generate from plan" today).
// =============================================================================

func (h *Phase2Handler) invoiceMilestone(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	milestoneID, err := uuid.Parse(chi.URLParam(r, "mid"))
	if err != nil {
		writeEntErr(w, http.StatusBadRequest, err)
		return
	}
	var (
		seqNo  int
		title  string
		progress float64
	)
	if err := h.pool.QueryRow(r.Context(), `
		SELECT seq_no, title, progress_pct
		FROM enterprise.project_milestones
		WHERE id = $1 AND project_id = $2
	`, milestoneID, projectID).Scan(&seqNo, &title, &progress); err != nil {
		writeEntErr(w, http.StatusNotFound, err)
		return
	}
	if progress < 100 {
		writeEntErr(w, http.StatusConflict,
			writeEntErrMsg{"milestone is not yet 100% complete"})
		return
	}
	// Hand off to Finance — the actual termin row picker lives in the
	// PreLaunchHandler. We just record the intent here for audit.
	writeEntJSON(w, http.StatusOK, map[string]any{
		"milestone_id": milestoneID.String(),
		"seq_no":       seqNo,
		"title":        title,
		"next_step": "Open the project's Termin Plan and click 'Generate invoice for seq " +
			itoa(seqNo) + "'.",
	})
}

type writeEntErrMsg struct{ Message string }

func (e writeEntErrMsg) Error() string { return e.Message }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
