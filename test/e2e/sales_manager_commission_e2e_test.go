// Sales-manager commission row.
//
// The existing TestCrossBranchCommission asserts 4 of the 5 commission
// rows; the sales_manager row is optional and didn't fire there because
// no reports_to chain was wired. This test wires it: a sales_manager
// user, then a sales_rep with reports_to_user_id pointing at the
// manager. After the first OTC payment, the 5-row split should land
// exactly:
//
//	sales_person          15%   sales rep
//	sales_manager          5%   manager (via reports_to walk)
//	sales_branch          10%   sales branch (rep's branch)
//	infrastructure_branch  ?    folded into company in the same-branch case
//	company               75%   60% + 10% infra fold-in + 0 (no cross)
//
// Same-branch keeps the test small (one branch, one ODP); cross-branch
// is locked in by the existing TestCrossBranchCommission.
//
//go:build e2e

package e2e

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSalesManagerCommission(t *testing.T) {
	c := newClient(t)
	c.login()

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	var (
		regionalID, branchID            string
		managerID, salesUserID          string
		leadID, customerID, orderID     string
	)

	t.Run("S01_one_branch", func(t *testing.T) {
		var reg struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "SM Regional " + sx, "code": "SM-REG-" + sx,
			"level": "regional",
		}, &reg, 201)
		regionalID = reg.ID

		var a struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "SM Area " + sx, "code": "SM-AREA-" + sx,
			"level": "area", "parent_id": regionalID,
		}, &a, 201)
		branchID = a.ID
	})

	t.Run("S02_manager_then_rep_reporting_to_manager", func(t *testing.T) {
		var mgr struct{ ID string `json:"id"` }
		// branch_level intentionally omitted — verifies the round-3
		// auto-derive (CreateUser pulls Level from the branch row).
		c.do("POST", "/api/identity/users", map[string]any{
			"employee_id": "SMMGR" + sx,
			"full_name":   "SM Manager " + sx,
			"email":       "smmgr" + sx + "@ion.local",
			"phone":       "+62811SM" + sx,
			"password":    "Pass1234!" + sx,
			"branch_id":   branchID,
			"roles":       []string{"sales_manager"},
		}, &mgr, 201)
		managerID = mgr.ID

		var rep struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/users", map[string]any{
			"employee_id":   "SMREP" + sx,
			"full_name":     "SM Rep " + sx,
			"email":         "smrep" + sx + "@ion.local",
			"phone":         "+62812SM" + sx,
			"password":      "Pass1234!" + sx,
			"branch_id":     branchID,
			"roles":         []string{"sales_rep"},
			"sales_type":    "broadband",
			"reports_to_id": managerID,
		}, &rep, 201)
		salesUserID = rep.ID
	})

	t.Run("S03_odp_in_same_branch", func(t *testing.T) {
		var types struct {
			Items []struct {
				ID      string `json:"id"`
				TypeKey string `json:"type_key"`
			} `json:"items"`
		}
		c.do("GET", "/api/network/node-types", nil, &types, 200)
		var odpTypeID string
		for _, nt := range types.Items {
			if nt.TypeKey == "odp" {
				odpTypeID = nt.ID
			}
		}
		var node struct{ ID string `json:"id"` }
		c.do("POST", "/api/network/nodes", map[string]any{
			"node_type_id":      odpTypeID,
			"name":              "SM ODP " + sx,
			"code":              "SM-ODP-" + sx,
			"branch_id":         branchID,
			"address":           "sales-manager test",
			"gps_lat":           odpLat,
			"gps_lng":           odpLng,
			"total_ports":       8,
			"port_role":         "customer_drop",
			"coverage_radius_m": 200,
		}, &node, 201)
	})

	t.Run("S04_lead_and_convert", func(t *testing.T) {
		var products struct {
			Items []struct{ ID string } `json:"items"`
		}
		c.do("GET", "/api/crm/products?active_only=true", nil, &products, 200)
		var lead struct {
			ID        string `json:"id"`
			Documents []struct {
				ID       string `json:"id"`
				Required bool   `json:"required"`
			} `json:"documents"`
		}
		c.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "SM Customer " + sx,
			"phone":      "+62813SM" + sx,
			"nik":        "31740" + sx + "7777",
			"address":    "Jl. SalesMgr " + sx,
			"gps_lat":    leadLat,
			"gps_lng":    leadLng,
			"product_id": products.Items[0].ID,
			"sales_id":   salesUserID,
		}, &lead, 201)
		leadID = lead.ID
		for _, d := range lead.Documents {
			if d.Required {
				c.do("PATCH", "/api/crm/documents/"+d.ID,
					map[string]any{"submitted": true}, nil, 200)
			}
		}
		var conv struct {
			Customer struct{ ID string } `json:"customer"`
			Order    struct{ ID string } `json:"order"`
		}
		c.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		customerID = conv.Customer.ID
		orderID = conv.Order.ID
		_ = customerID
	})

	t.Run("S05_pay_otc_and_assert_manager_row", func(t *testing.T) {
		var invs struct {
			Items []struct {
				ID    string  `json:"id"`
				Total float64 `json:"total"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/invoices?order_id="+orderID, nil, &invs, 200)
		if len(invs.Items) == 0 {
			t.Fatal("no OTC invoice on the converted order")
		}
		inv := invs.Items[0]
		c.do("POST", "/api/billing/invoices/"+inv.ID+"/payments", map[string]any{
			"amount":         inv.Total,
			"payment_method": "manual_bank_transfer",
			"notes":          "sales-manager e2e",
		}, nil, 200)

		dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
		pool, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			t.Fatalf("pgx pool: %v", err)
		}
		defer pool.Close()
		rows, err := pool.Query(context.Background(), `
			SELECT party_type, user_id, branch_id, amount, percentage,
			       COALESCE(notes, '')
			  FROM billing.commission_records
			 WHERE order_id = $1
			 ORDER BY party_type
		`, uuid.MustParse(orderID))
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		type rec struct {
			party    string
			userID   *string
			branchID *string
			amount   float64
			percent  float64
			notes    string
		}
		seen := map[string]rec{}
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.party, &r.userID, &r.branchID,
				&r.amount, &r.percent, &r.notes); err != nil {
				t.Fatalf("scan: %v", err)
			}
			seen[r.party] = r
		}

		// Required rows.
		for _, party := range []string{"sales_person", "sales_manager", "sales_branch", "company"} {
			if _, ok := seen[party]; !ok {
				t.Errorf("missing commission row party=%q (got %d rows: %v)",
					party, len(seen), keys(seen))
			}
		}

		if r, ok := seen["sales_person"]; ok {
			if r.userID == nil || *r.userID != salesUserID {
				t.Errorf("sales_person user_id: want %s, got %v", salesUserID, r.userID)
			}
			if r.percent != 15.0 {
				t.Errorf("sales_person percent: want 15, got %v", r.percent)
			}
		}

		if r, ok := seen["sales_manager"]; ok {
			if r.userID == nil || *r.userID != managerID {
				t.Errorf("sales_manager user_id: want %s (the reports_to target), got %v",
					managerID, r.userID)
			}
			if r.percent != 5.0 {
				t.Errorf("sales_manager percent: want 5, got %v", r.percent)
			}
		}

		if r, ok := seen["sales_branch"]; ok {
			if r.branchID == nil || *r.branchID != branchID {
				t.Errorf("sales_branch branch_id: want %s, got %v", branchID, r.branchID)
			}
			if r.percent != 10.0 {
				t.Errorf("sales_branch percent: want 10, got %v", r.percent)
			}
		}

		// In the same-branch case, the 10% infra share folds into the
		// company bucket → company = 60 + 10 = 70%. There should be
		// no separate infrastructure_branch row.
		if _, ok := seen["infrastructure_branch"]; ok {
			t.Errorf("unexpected infrastructure_branch row in same-branch case")
		}
		if r, ok := seen["company"]; ok {
			if r.percent != 70.0 {
				t.Errorf("company percent in same-branch with manager: want 70, got %v",
					r.percent)
			}
		}
	})
}

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
