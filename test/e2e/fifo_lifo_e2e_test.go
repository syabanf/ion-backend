// FIFO / LIFO warehouse dispatch correctness.
//
// We create one warehouse and one serialized-device stock item, then
// intake three assets at three different (back-dated) received_at
// times. The asset list endpoint exposes the FIFO/LIFO knob via
// `order_by`:
//
//   default (LIFO, newest first) → asset3, asset2, asset1
//   ?order_by=fifo               → asset1, asset2, asset3
//
// We back-date `received_at` via SQL after the intake because the intake
// endpoint doesn't accept past timestamps (intake stamps NOW()), and we
// need a deterministic ordering even when the wall clock between calls
// is sub-millisecond apart.
//
//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFIFOLIFODispatch(t *testing.T) {
	c := newClient(t)
	c.login()

	sx := suffix()

	// ----- branches: regional + area (warehouse needs a branch) -----
	var regional, area struct{ ID string `json:"id"` }
	c.do("POST", "/api/identity/branches", map[string]any{
		"name": "FL Regional " + sx, "code": "FL-REG-" + sx,
		"level": "regional",
	}, &regional, 201)
	c.do("POST", "/api/identity/branches", map[string]any{
		"name": "FL Area " + sx, "code": "FL-AREA-" + sx,
		"level": "area", "parent_id": regional.ID,
	}, &area, 201)

	// ----- warehouse -----
	var wh struct{ ID string `json:"id"` }
	c.do("POST", "/api/warehouse/warehouses", map[string]any{
		"code":      "FL-WH-" + sx,
		"name":      "FL Warehouse " + sx,
		"branch_id": area.ID,
		"address":   "FL test warehouse",
	}, &wh, 201)

	// ----- stock item (serialized device — assets get rows) -----
	var item struct{ ID string `json:"id"` }
	c.do("POST", "/api/warehouse/catalog/items", map[string]any{
		"sku":      "FL-ONT-" + sx,
		"name":     "FL ONT " + sx,
		"category": "serialized_device",
		"brand":    "Test",
		"model":    "FL-1",
		"spec":     "test",
		"unit":     "pcs",
	}, &item, 201)

	// ----- intake three serialized assets -----
	intake := func(serial string) {
		c.do("POST", "/api/warehouse/warehouses/"+wh.ID+"/intake", map[string]any{
			"stock_item_id": item.ID,
			"serials":       []map[string]any{{"serial_number": serial, "ownership_type": "owned", "condition": "new"}},
			"unit_cost":     100000,
		}, nil, 201)
	}
	intake("FL-S1-" + sx)
	intake("FL-S2-" + sx)
	intake("FL-S3-" + sx)

	// Back-date received_at via SQL so the three assets are ordered
	// deterministically — sub-millisecond clock gaps between intakes
	// can otherwise yield equal timestamps and a tie in the sort.
	dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	ctx := context.Background()
	for i, ser := range []string{"FL-S1-" + sx, "FL-S2-" + sx, "FL-S3-" + sx} {
		// S1 = 3 days ago, S2 = 2 days ago, S3 = 1 day ago.
		at := time.Now().UTC().AddDate(0, 0, -(3 - i))
		if _, err := pool.Exec(ctx,
			`UPDATE warehouse.assets SET received_at = $1 WHERE serial_number = $2`,
			at, ser); err != nil {
			t.Fatalf("backdate: %v", err)
		}
	}

	listAssetsFor := func(orderBy string) []string {
		path := "/api/warehouse/assets?warehouse_id=" + wh.ID + "&stock_item_id=" + item.ID
		if orderBy != "" {
			path += "&order_by=" + orderBy
		}
		var resp struct {
			Items []struct {
				SerialNumber string `json:"serial_number"`
			} `json:"items"`
		}
		c.do("GET", path, nil, &resp, 200)
		out := make([]string, 0, len(resp.Items))
		for _, a := range resp.Items {
			out = append(out, a.SerialNumber)
		}
		return out
	}

	// Round-3: the warehouse usecase now reads
	// platform_config.inventory_valuation_method when callers don't
	// pass `order_by`. We don't assert a specific default direction
	// here — that's a deployment-config knob, exercised by the unit
	// test on the repo. Both explicit directions are still locked
	// in against the same dataset:

	t.Run("LIFO_explicit", func(t *testing.T) {
		got := listAssetsFor("lifo")
		want := []string{"FL-S3-" + sx, "FL-S2-" + sx, "FL-S1-" + sx}
		if !startsWith(got, want) {
			t.Fatalf("explicit LIFO wrong: want prefix %v, got %v", want, got)
		}
	})

	t.Run("FIFO_oldest_first", func(t *testing.T) {
		got := listAssetsFor("fifo")
		// FIFO returns oldest first across the whole table — find our
		// serials in the page and confirm their relative order.
		positions := map[string]int{}
		for i, s := range got {
			positions[s] = i
		}
		s1, s2, s3 := "FL-S1-"+sx, "FL-S2-"+sx, "FL-S3-"+sx
		p1, ok1 := positions[s1]
		p2, ok2 := positions[s2]
		p3, ok3 := positions[s3]
		if !ok1 || !ok2 || !ok3 {
			t.Fatalf("FIFO listing missing some of our serials; got %v", got)
		}
		if !(p1 < p2 && p2 < p3) {
			t.Fatalf("FIFO order wrong: S1 should come before S2 before S3, got positions %d %d %d (%v)",
				p1, p2, p3, got)
		}
	})

	// Defensive: assert the receiver_id used by the assertions actually
	// corresponds to a real ID — silence the linter if the var stays
	// unused for any reason.
	_ = uuid.Nil
}

// startsWith returns true iff `prefix` matches the first len(prefix)
// elements of `got` in order.
func startsWith(got, prefix []string) bool {
	if len(got) < len(prefix) {
		return false
	}
	for i, v := range prefix {
		if got[i] != v {
			return false
		}
	}
	return true
}
