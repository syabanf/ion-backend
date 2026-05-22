// Speedtest checklist parser.
//
// The tech app collects a "speedtest" checklist item on broadband
// new-install + maintenance WOs. The numbers get pipe-encoded into
// wo_checklist_responses.response_text as:
//
//   down=85.2|up=42.7|ping_ms=12
//
// We decode them here on read so the dashboard + NOC can render them
// without baking a special-case column into the schema. If the encoding
// changes (e.g. when we move to structured JSON), only this file
// changes.
//
// Two endpoints:
//
//   GET /work-orders/{id}/speedtest   — one WO's parsed speedtest
//   GET /speedtests/recent            — last 100 across the fleet
//
// Both filter by item_type='speedtest' so non-speedtest checklist
// rows don't bleed into the response.
package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// SpeedtestResult is the parsed shape. Any field can be nil when the
// tech submitted a partial speedtest (e.g. WAN dropped before upload).
type SpeedtestResult struct {
	WoID         string     `json:"wo_id"`
	WoNumber     string     `json:"wo_number,omitempty"`
	DownMbps     *float64   `json:"down_mbps,omitempty"`
	UpMbps       *float64   `json:"up_mbps,omitempty"`
	PingMs       *float64   `json:"ping_ms,omitempty"`
	Raw          string     `json:"raw"` // original response_text for debugging
	CapturedAt   *time.Time `json:"captured_at,omitempty"`
	TechUserID   *string    `json:"tech_user_id,omitempty"`
	TechFullName string     `json:"tech_full_name,omitempty"`
}

// parseSpeedtestText splits "down=X|up=Y|ping_ms=Z" (in any order, with
// any subset present) into a SpeedtestResult. Returns false if no
// recognisable speedtest fields were found.
func parseSpeedtestText(s string) (SpeedtestResult, bool) {
	r := SpeedtestResult{Raw: s}
	if s == "" {
		return r, false
	}
	found := false
	for _, part := range strings.Split(s, "|") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			continue
		}
		switch key {
		case "down", "down_mbps", "download":
			r.DownMbps = &f
			found = true
		case "up", "up_mbps", "upload":
			r.UpMbps = &f
			found = true
		case "ping_ms", "ping", "latency_ms":
			r.PingMs = &f
			found = true
		}
	}
	return r, found
}

func (h *Phase2Handler) getWorkOrderSpeedtest(w http.ResponseWriter, r *http.Request) {
	woID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	// Pick the latest speedtest response on the WO. There may be more
	// than one (re-run after fix) — caller wants the most recent.
	var (
		respText    string
		capturedAt  *time.Time
		techID      *uuid.UUID
		woNumber    string
	)
	err = h.pool.QueryRow(r.Context(), `
		SELECT COALESCE(wcr.response_text, ''),
		       wcr.submitted_at,
		       wcr.submitted_by,
		       COALESCE(w.wo_number, '')
		FROM field.wo_checklist_responses wcr
		JOIN field.wo_checklist_template_items cti
		    ON cti.id = wcr.template_item_id
		JOIN field.work_orders w ON w.id = wcr.wo_id
		WHERE wcr.wo_id = $1
		  AND cti.item_type = 'speedtest'
		ORDER BY wcr.submitted_at DESC NULLS LAST
		LIMIT 1
	`, woID).Scan(&respText, &capturedAt, &techID, &woNumber)
	if err != nil {
		// No speedtest response — return an empty 200 so the dashboard
		// can render "no speedtest captured" without a noisy 404.
		writeFieldJSON(w, http.StatusOK, map[string]any{
			"wo_id": woID.String(),
			"found": false,
		})
		return
	}
	res, ok := parseSpeedtestText(respText)
	res.WoID = woID.String()
	res.WoNumber = woNumber
	res.CapturedAt = capturedAt
	if techID != nil {
		s := techID.String()
		res.TechUserID = &s
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{
		"wo_id":     woID.String(),
		"found":     ok,
		"speedtest": res,
	})
}

func (h *Phase2Handler) listRecentSpeedtests(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT wcr.wo_id,
		       COALESCE(w.wo_number, ''),
		       COALESCE(wcr.response_text, ''),
		       wcr.submitted_at,
		       wcr.submitted_by,
		       COALESCE(u.full_name, u.email, '')
		FROM field.wo_checklist_responses wcr
		JOIN field.wo_checklist_template_items cti
		    ON cti.id = wcr.template_item_id
		JOIN field.work_orders w ON w.id = wcr.wo_id
		LEFT JOIN identity.users u ON u.id = wcr.submitted_by
		WHERE cti.item_type = 'speedtest'
		  AND wcr.response_text IS NOT NULL
		  AND wcr.response_text != ''
		ORDER BY wcr.submitted_at DESC NULLS LAST
		LIMIT 100
	`)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []SpeedtestResult{}
	for rows.Next() {
		var (
			woID       uuid.UUID
			woNumber   string
			respText   string
			capturedAt *time.Time
			techID     *uuid.UUID
			techName   string
		)
		if err := rows.Scan(&woID, &woNumber, &respText, &capturedAt, &techID, &techName); err != nil {
			continue
		}
		res, ok := parseSpeedtestText(respText)
		if !ok {
			continue
		}
		res.WoID = woID.String()
		res.WoNumber = woNumber
		res.CapturedAt = capturedAt
		res.TechFullName = techName
		if techID != nil {
			s := techID.String()
			res.TechUserID = &s
		}
		out = append(out, res)
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}
