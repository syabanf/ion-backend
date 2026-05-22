// Bulk invoice perf — measures how long it takes to mint N invoices
// directly through the billing-svc HTTP API. The Phase-1 NFR target
// is 10k invoices in 10 minutes (≈ 16.7 invoices/sec sustained).
//
// We don't run this on the default `go test`; the build tag `perf`
// gates it. Run with:
//
//	make test-perf BENCH=BenchmarkBulkInvoice INVOICES=10000
//
// Behaviour:
//
//   - logs in once as super-admin
//   - creates one ad-hoc customer + order via SQL (skipping the slow
//     coverage + KTP + branch setup the broadband E2E walks through)
//   - POSTs the configured number of OTC invoices, one per request
//   - reports duration + invoices/sec; fails if below the NFR ceiling
//
// `INVOICES` defaults to 200 so the smoke flow is quick; the 10k run
// only happens when ops explicitly bumps it. We don't go through the
// recurring scheduler here — that path is exercised in
// `test/e2e/auto_termination_e2e_test.go`; this bench focuses on the
// invoice-create code path itself, which is the hot one when ops
// migrates from a legacy system.
//
//go:build perf

package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const baseURL = "http://localhost:8080"

func TestBulkInvoiceThroughput(t *testing.T) {
	count := 200
	if v := os.Getenv("INVOICES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("INVOICES=%q invalid", v)
		}
		count = n
	}

	token := login(t)

	dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	customerID, orderID := provisionCustomerOrder(t, pool)

	client := &http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	for i := 0; i < count; i++ {
		body := map[string]any{
			"customer_id": customerID,
			"order_id":    orderID,
			// recurring matches the production hot path — the scheduler
			// mints one recurring invoice per active customer each month.
			// OTC has a unique-per-order constraint that blocks bulk
			// inserts on the same order, so it isn't a viable bench
			// shape; the recurring path is.
			"invoice_type": "recurring",
			"ppn_rate":     11.0,
			"due_date":     time.Now().AddDate(0, 0, 7).Format("2006-01-02"),
			"issue":        true,
			"lines": []map[string]any{
				{
					"description": fmt.Sprintf("perf test line %d", i),
					"item_type":   "mrc",
					"quantity":    1,
					"unit_price":  100000,
				},
			},
		}
		buf, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", baseURL+"/api/billing/invoices", bytes.NewReader(buf))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("invoice %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("invoice %d: status %d — %s", i, resp.StatusCode, string(body))
		}
		resp.Body.Close()
	}
	elapsed := time.Since(start)
	rate := float64(count) / elapsed.Seconds()

	t.Logf("bulk-invoice: %d invoices in %s = %.1f/sec", count, elapsed, rate)
	t.Logf("NFR target  : 10000 in 10 min = 16.7/sec; this run = %s for 10k",
		time.Duration(float64(10_000)/rate*float64(time.Second)))

	// Soft failure: only enforce the target when the operator explicitly
	// asks for the 10k run. Smaller probes log the rate for inspection.
	if count >= 10_000 {
		if rate < 16.7 {
			t.Fatalf("rate %.1f/sec is below the 16.7/sec NFR target", rate)
		}
	}
}

// provisionCustomerOrder side-steps the slow CRM convert flow by
// inserting a customer + order directly via SQL. The bench measures
// only the invoice-create path; coverage + KTP + branch setup are
// exercised in the broadband happy path.
func provisionCustomerOrder(t *testing.T, pool *pgxpool.Pool) (customerID, orderID string) {
	ctx := context.Background()
	cID := uuid.New()
	oID := uuid.New()
	custNum := "PERF-CUST-" + uuid.NewString()[:8]
	orderNum := "PERF-ORD-" + uuid.NewString()[:8]

	if _, err := pool.Exec(ctx, `
		INSERT INTO crm.customers (id, customer_number, customer_type,
		    full_name, phone, address, status)
		VALUES ($1, $2, 'broadband', 'Perf Customer', '+62811000000',
		        'perf bench address', 'active')
	`, cID, custNum); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO crm.orders (id, order_number, customer_id,
		    monthly_price, otc_price, status)
		VALUES ($1, $2, $3, 350000, 750000, 'active')
	`, oID, orderNum, cID); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return cID.String(), oID.String()
}

func login(t *testing.T) string {
	email := envOr("SEED_ADMIN_EMAIL", "admin@ion.local")
	pwd := envOr("SEED_ADMIN_PASSWORD", "IonAdmin#2026!ChangeMe")
	body, _ := json.Marshal(map[string]string{"email": email, "password": pwd})
	resp, err := http.Post(baseURL+"/api/identity/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v (is the stack up on :8080?)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	var out struct{ AccessToken string `json:"access_token"` }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("login decode: %v", err)
	}
	return out.AccessToken
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
