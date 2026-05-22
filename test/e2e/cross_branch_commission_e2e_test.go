// Cross-branch commission end-to-end.
//
// Existing happy-path coverage exercises the same-branch case where
// the infrastructure_branch share (10%) folds into the company bucket.
// This test exercises the genuinely cross-branch path:
//
//   sales user lives in BRANCH-A
//   ODP (and therefore the order) lives in BRANCH-B
//   OTC paid → commission_records has five rows:
//     sales_person       (15%, sales user)
//     sales_manager       (5%, walked via reports_to — skipped here)
//     sales_branch       (10%, BRANCH-A)
//     infrastructure_branch (10%, BRANCH-B)  ← the cross-branch row
//     company            (60%)
//
// sales_manager is optional — the chain may not include one — so we
// don't assert on it here.
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

func TestCrossBranchCommission(t *testing.T) {
	c := newClient(t)
	c.login()

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	var (
		branchA, branchB, regionalID string
		salesUserID                  string
		leadID, customerID, orderID  string
	)

	t.Run("S01_two_branches", func(t *testing.T) {
		var reg struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "XB Regional " + sx, "code": "XB-REG-" + sx,
			"level": "regional",
		}, &reg, 201)
		regionalID = reg.ID

		var a, b struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "XB Area A " + sx, "code": "XB-A-" + sx,
			"level": "area", "parent_id": regionalID,
		}, &a, 201)
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "XB Area B " + sx, "code": "XB-B-" + sx,
			"level": "area", "parent_id": regionalID,
		}, &b, 201)
		branchA = a.ID
		branchB = b.ID
		_ = branchA
	})

	t.Run("S02_sales_user_in_branch_a", func(t *testing.T) {
		var u struct{ ID string `json:"id"` }
		// branch_level intentionally omitted — verifies the round-3
		// auto-derive (CreateUser pulls Level from the branch row).
		c.do("POST", "/api/identity/users", map[string]any{
			"employee_id": "XBSR" + sx,
			"full_name":   "XB Sales " + sx,
			"email":       "xbs" + sx + "@ion.local",
			"phone":       "+62811XB" + sx,
			"password":    "Pass1234!" + sx,
			"branch_id":   branchA,
			"roles":       []string{"sales_rep"},
			"sales_type":  "broadband",
		}, &u, 201)
		salesUserID = u.ID
	})

	t.Run("S03_odp_in_branch_b", func(t *testing.T) {
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
			"name":              "XB ODP " + sx,
			"code":              "XB-ODP-" + sx,
			"branch_id":         branchB,
			"address":           "cross-branch ODP",
			"gps_lat":           odpLat,
			"gps_lng":           odpLng,
			"total_ports":       8,
			"port_role":         "customer_drop",
			"coverage_radius_m": 200,
		}, &node, 201)
	})

	t.Run("S04_lead_owned_by_sales_user", func(t *testing.T) {
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
			BranchID *string `json:"branch_id"`
		}
		c.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "XB Customer " + sx,
			"phone":      "+62812XB" + sx,
			"nik":        "31740" + sx + "5555",
			"address":    "Jl. CrossBranch " + sx,
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
		if lead.BranchID == nil || *lead.BranchID != branchB {
			// The lead's branch is set from coverage_snapshot's best
			// candidate. We placed the ODP in branchB, so coverage
			// should resolve to branchB. If the test gets the wrong
			// branch here, the commission split won't be cross-branch.
			got := "<nil>"
			if lead.BranchID != nil {
				got = *lead.BranchID
			}
			t.Fatalf("lead branch_id: want %s (branchB), got %s", branchB, got)
		}
	})

	t.Run("S05_convert_and_inherit_order_branch", func(t *testing.T) {
		var conv struct {
			Customer struct{ ID string } `json:"customer"`
			Order    struct {
				ID       string  `json:"id"`
				BranchID *string `json:"branch_id"`
				SalesID  *string `json:"sales_id"`
			} `json:"order"`
		}
		c.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		customerID = conv.Customer.ID
		orderID = conv.Order.ID
		if conv.Order.BranchID == nil || *conv.Order.BranchID != branchB {
			t.Fatalf("order branch_id: want %s, got %v", branchB, conv.Order.BranchID)
		}
		if conv.Order.SalesID == nil || *conv.Order.SalesID != salesUserID {
			t.Fatalf("order sales_id: want %s, got %v", salesUserID, conv.Order.SalesID)
		}
	})

	t.Run("S06_pay_otc_and_assert_cross_branch_split", func(t *testing.T) {
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
			"notes":          "cross-branch commission e2e",
		}, nil, 200)

		// The commission hook fires synchronously inside RecordPayment.
		// Pull the rows directly from the DB — the read API filters by
		// user / branch / order, which we'd use anyway, but going to
		// the DB gives us the full set in one round-trip.
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
			t.Fatalf("query commissions: %v", err)
		}
		defer rows.Close()
		type rec struct {
			party     string
			userID    *string
			branchID  *string
			amount    float64
			percent   float64
			notes     string
		}
		var got []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.party, &r.userID, &r.branchID,
				&r.amount, &r.percent, &r.notes); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, r)
		}

		// Always expect sales_person, sales_branch, infrastructure_branch,
		// company. sales_manager is optional (reports_to walk may miss).
		seen := map[string]rec{}
		for _, r := range got {
			seen[r.party] = r
		}

		if r, ok := seen["sales_person"]; !ok {
			t.Errorf("missing sales_person commission row")
		} else if r.userID == nil || *r.userID != salesUserID {
			t.Errorf("sales_person row not bound to the sales user; user_id=%v", r.userID)
		}

		if r, ok := seen["sales_branch"]; !ok {
			t.Errorf("missing sales_branch commission row")
		} else if r.branchID == nil || *r.branchID != branchA {
			t.Errorf("sales_branch row branch_id: want %s (branchA), got %v",
				branchA, r.branchID)
		}

		if r, ok := seen["infrastructure_branch"]; !ok {
			t.Errorf("missing infrastructure_branch row — cross-branch split didn't fire")
		} else {
			if r.branchID == nil || *r.branchID != branchB {
				t.Errorf("infrastructure_branch branch_id: want %s (branchB), got %v",
					branchB, r.branchID)
			}
			if r.percent != 10.0 {
				t.Errorf("infrastructure_branch percent: want 10, got %v", r.percent)
			}
		}

		if r, ok := seen["company"]; !ok {
			t.Errorf("missing company commission row")
		} else if r.percent != 60.0 {
			// In the cross-branch case the company share stays at 60%
			// — the 10% infrastructure share doesn't fold in.
			t.Errorf("company percent in cross-branch split: want 60, got %v", r.percent)
		}
	})

	// customerID is referenced for clarity but the assertion already
	// runs against the order.
	_ = customerID
}
