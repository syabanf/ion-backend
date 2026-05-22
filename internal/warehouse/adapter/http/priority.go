// Warehouse priority-followup endpoints: cross-warehouse stock
// roll-up + low-stock alerts feed.
package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

type PriorityHandler struct {
	pool     *pgxpool.Pool
	verifier *auth.Verifier
}

func NewPriorityHandler(pool *pgxpool.Pool, verifier *auth.Verifier) *PriorityHandler {
	return &PriorityHandler{pool: pool, verifier: verifier}
}

func (h *PriorityHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("warehouse.stock_dashboard.read")).
			Get("/stock-dashboard", h.stockDashboard)
		r.With(httpserver.RequirePermission("warehouse.stock_dashboard.read")).
			Get("/stock-dashboard/alerts", h.stockAlerts)

		// Cross-warehouse opname roll-up — sessions + variance.
		r.With(httpserver.RequirePermission("warehouse.opname.read.rollup")).
			Get("/opname-rollup", h.opnameRollup)
	})
}

// opnameRollup unions every active opname session with per-warehouse
// progress + total variance count. Lets ops see "where are we" in
// the monthly count without clicking into each warehouse.
func (h *PriorityHandler) opnameRollup(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT
		    s.id AS session_id,
		    s.warehouse_id,
		    w.name AS warehouse_name,
		    w.code AS warehouse_code,
		    s.status,
		    s.started_at,
		    COALESCE(s.committed_at, s.cancelled_at) AS finished_at,
		    (SELECT COUNT(*) FROM warehouse.opname_counts oc
		     WHERE oc.session_id = s.id) AS items_counted,
		    (SELECT COUNT(*) FROM warehouse.opname_counts oc
		     WHERE oc.session_id = s.id
		       AND oc.variance != 0) AS items_with_variance
		FROM warehouse.opname_sessions s
		JOIN warehouse.warehouses w ON w.id = s.warehouse_id
		WHERE w.active = TRUE
		ORDER BY s.started_at DESC NULLS LAST
		LIMIT 100
	`)
	if err != nil {
		writeStockErr(w, err)
		return
	}
	defer rows.Close()
	type row struct {
		SessionID         string  `json:"session_id"`
		WarehouseID       string  `json:"warehouse_id"`
		WarehouseName     string  `json:"warehouse_name"`
		WarehouseCode     string  `json:"warehouse_code"`
		Status            string  `json:"status"`
		StartedAt         *string `json:"started_at,omitempty"`
		CompletedAt       *string `json:"completed_at,omitempty"`
		ItemsCounted      int     `json:"items_counted"`
		ItemsWithVariance int     `json:"items_with_variance"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var started, completed *string
		if err := rows.Scan(&x.SessionID, &x.WarehouseID, &x.WarehouseName,
			&x.WarehouseCode, &x.Status, &started, &completed,
			&x.ItemsCounted, &x.ItemsWithVariance); err == nil {
			x.StartedAt = started
			x.CompletedAt = completed
			out = append(out, x)
		}
	}
	writeStockJSON(w, http.StatusOK, map[string]any{"items": out})
}

// stockDashboard — one summary row per warehouse with on-hand totals
// + low-stock SKU count vs per-level threshold.
func (h *PriorityHandler) stockDashboard(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT
		    w.id, w.name, w.code, COALESCE(w.address, ''),
		    COALESCE(SUM(sl.quantity), 0) AS total_on_hand,
		    COUNT(DISTINCT sl.stock_item_id) FILTER (WHERE sl.quantity > 0) AS skus_in_stock,
		    COUNT(DISTINCT sl.stock_item_id) FILTER (
		        WHERE sl.min_threshold IS NOT NULL
		          AND sl.quantity < sl.min_threshold
		    ) AS skus_below_threshold
		FROM warehouse.warehouses w
		LEFT JOIN warehouse.stock_levels sl ON sl.warehouse_id = w.id
		WHERE w.active = TRUE
		GROUP BY w.id, w.name, w.code, w.address
		ORDER BY w.name ASC
	`)
	if err != nil {
		writeStockErr(w, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID                 string  `json:"id"`
		Name               string  `json:"name"`
		Code               string  `json:"code"`
		Address            string  `json:"address"`
		TotalOnHand        float64 `json:"total_on_hand"`
		SkusInStock        int     `json:"skus_in_stock"`
		SkusBelowThreshold int     `json:"skus_below_threshold"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Name, &x.Code, &x.Address,
			&x.TotalOnHand, &x.SkusInStock, &x.SkusBelowThreshold); err == nil {
			out = append(out, x)
		}
	}
	writeStockJSON(w, http.StatusOK, map[string]any{"items": out})
}

// stockAlerts — every (warehouse, SKU) row currently below threshold.
func (h *PriorityHandler) stockAlerts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT
		    w.id, w.name, w.code,
		    si.id, si.sku, si.name,
		    sl.quantity, COALESCE(sl.min_threshold, 0)
		FROM warehouse.stock_levels sl
		JOIN warehouse.warehouses w ON w.id = sl.warehouse_id
		JOIN warehouse.stock_items si ON si.id = sl.stock_item_id
		WHERE w.active = TRUE
		  AND sl.min_threshold IS NOT NULL
		  AND sl.quantity < sl.min_threshold
		ORDER BY sl.quantity ASC, w.name ASC
		LIMIT 200
	`)
	if err != nil {
		writeStockErr(w, err)
		return
	}
	defer rows.Close()
	type row struct {
		WarehouseID   string  `json:"warehouse_id"`
		WarehouseName string  `json:"warehouse_name"`
		WarehouseCode string  `json:"warehouse_code"`
		CatalogID     string  `json:"catalog_id"`
		CatalogCode   string  `json:"catalog_code"`
		CatalogName   string  `json:"catalog_name"`
		OnHand        float64 `json:"on_hand"`
		MinThreshold  float64 `json:"min_threshold"`
		Severity      string  `json:"severity"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.WarehouseID, &x.WarehouseName, &x.WarehouseCode,
			&x.CatalogID, &x.CatalogCode, &x.CatalogName, &x.OnHand, &x.MinThreshold); err == nil {
			switch {
			case x.OnHand <= 0:
				x.Severity = "out"
			case x.OnHand < x.MinThreshold*0.5:
				x.Severity = "critical"
			default:
				x.Severity = "low"
			}
			out = append(out, x)
		}
	}
	writeStockJSON(w, http.StatusOK, map[string]any{"items": out})
}

func writeStockJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeStockErr(w http.ResponseWriter, err error) {
	writeStockJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]string{"message": err.Error()},
	})
}
