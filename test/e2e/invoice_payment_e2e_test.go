// Invoice + payment lifecycle end-to-end.
//
// Wave 50 — the broadband happy-path test asserts the auto-OTC invoice
// is created on lead-conversion but stops short of actually paying it.
// This test takes over from there:
//
//   1. setupCoveredCustomer (helper) — fresh branch/ODP/lead/customer
//      with an auto-OTC invoice already issued
//   2. List invoices for the order — confirm the OTC is there and has
//      outstanding == total (nothing paid yet)
//   3. Record a partial payment — outstanding goes down by the payment
//      amount, status stays 'issued'
//   4. Record the remainder — outstanding goes to 0, status flips to
//      'paid'
//   5. Idempotency probe — POSTing the same gateway_transaction_id a
//      second time MUST NOT double-record (we accept either 200 with the
//      same payment id OR 409 — both are acceptable contracts; we just
//      assert the customer's outstanding doesn't go negative)
//
// This proves the Billing module's core contract: payment recording
// reduces outstanding, settles invoices atomically, and is idempotent
// on the gateway_transaction_id key.
//
//go:build e2e

package e2e

import (
	"fmt"
	"testing"
)

func TestInvoicePaymentLifecycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)

	// -----------------------------------------------------------------
	// 1. Confirm the auto-OTC invoice is present + unpaid.
	// -----------------------------------------------------------------
	var invList struct {
		Items []struct {
			ID                  string  `json:"id"`
			InvoiceNumber       string  `json:"invoice_number"`
			InvoiceType         string  `json:"invoice_type"`
			Status              string  `json:"status"`
			Total               float64 `json:"total"`
			PaidAmount          float64 `json:"paid_amount"`
			OutstandingAmount   float64 `json:"outstanding_amount"`
		} `json:"items"`
	}
	admin.do("GET", "/api/billing/invoices?order_id="+h.OrderID, nil, &invList, 200)
	if len(invList.Items) == 0 {
		t.Fatalf("no invoices for order %s — auto-OTC missing", h.OrderID)
	}
	otc := invList.Items[0]
	if otc.InvoiceType != "otc" {
		t.Fatalf("first invoice not OTC: got %q", otc.InvoiceType)
	}
	if otc.Total <= 0 {
		t.Fatalf("invoice total <= 0: %v", otc.Total)
	}
	if otc.PaidAmount != 0 {
		t.Fatalf("paid_amount != 0 on fresh invoice: %v", otc.PaidAmount)
	}
	if otc.OutstandingAmount != otc.Total {
		t.Fatalf("outstanding != total on fresh invoice: outstanding=%v total=%v",
			otc.OutstandingAmount, otc.Total)
	}

	// -----------------------------------------------------------------
	// 2. Partial payment — pay half. status remains 'issued'.
	// -----------------------------------------------------------------
	half := otc.Total / 2
	gwTx1 := "TX-W50-A-" + suffix()
	var pay1 struct {
		ID     string  `json:"id"`
		Amount float64 `json:"amount"`
	}
	admin.do("POST", fmt.Sprintf("/api/billing/invoices/%s/payments", otc.ID),
		map[string]any{
			"amount":                 half,
			"payment_method":         "manual",
			"gateway_transaction_id": gwTx1,
			"notes":                  "Wave 50 — partial payment",
		}, &pay1, 201)
	if pay1.ID == "" {
		t.Fatal("partial payment returned empty id")
	}

	var afterHalf struct {
		Status            string  `json:"status"`
		PaidAmount        float64 `json:"paid_amount"`
		OutstandingAmount float64 `json:"outstanding_amount"`
	}
	admin.do("GET", "/api/billing/invoices/"+otc.ID, nil, &afterHalf, 200)
	if afterHalf.Status == "paid" {
		t.Fatalf("invoice prematurely flipped to paid after partial: %v", afterHalf)
	}
	if !approxEqual(afterHalf.PaidAmount, half) {
		t.Errorf("paid_amount: want %v, got %v", half, afterHalf.PaidAmount)
	}
	if !approxEqual(afterHalf.OutstandingAmount, otc.Total-half) {
		t.Errorf("outstanding: want %v, got %v", otc.Total-half, afterHalf.OutstandingAmount)
	}

	// -----------------------------------------------------------------
	// 3. Settle the remainder — status flips to 'paid', outstanding == 0.
	// -----------------------------------------------------------------
	gwTx2 := "TX-W50-B-" + suffix()
	admin.do("POST", fmt.Sprintf("/api/billing/invoices/%s/payments", otc.ID),
		map[string]any{
			"amount":                 otc.Total - half,
			"payment_method":         "manual",
			"gateway_transaction_id": gwTx2,
			"notes":                  "Wave 50 — final payment",
		}, nil, 201)

	var afterFull struct {
		Status            string  `json:"status"`
		PaidAmount        float64 `json:"paid_amount"`
		OutstandingAmount float64 `json:"outstanding_amount"`
	}
	admin.do("GET", "/api/billing/invoices/"+otc.ID, nil, &afterFull, 200)
	if afterFull.Status != "paid" {
		t.Errorf("status: want paid, got %q", afterFull.Status)
	}
	if !approxEqual(afterFull.PaidAmount, otc.Total) {
		t.Errorf("paid_amount: want %v, got %v", otc.Total, afterFull.PaidAmount)
	}
	if !approxEqual(afterFull.OutstandingAmount, 0) {
		t.Errorf("outstanding: want 0, got %v", afterFull.OutstandingAmount)
	}

	// -----------------------------------------------------------------
	// 4. Idempotency probe — re-submitting gwTx2 must NOT double the
	//    payment. Either 409 or 200+same-id is acceptable; what matters
	//    is paid_amount stays at total.
	// -----------------------------------------------------------------
	resp, err := admin.http.Post(
		baseURL+"/api/billing/invoices/"+otc.ID+"/payments",
		"application/json",
		mustJSON(map[string]any{
			"amount":                 otc.Total - half,
			"payment_method":         "manual",
			"gateway_transaction_id": gwTx2,
			"notes":                  "duplicate submit — should be idempotent",
		}),
	)
	if err == nil {
		_ = resp.Body.Close()
	}
	// Whatever the backend does (idempotent accept, 409 conflict, or
	// even 200 with same id), the invoice MUST still be exactly paid.
	var afterDup struct {
		Status            string  `json:"status"`
		PaidAmount        float64 `json:"paid_amount"`
		OutstandingAmount float64 `json:"outstanding_amount"`
	}
	admin.do("GET", "/api/billing/invoices/"+otc.ID, nil, &afterDup, 200)
	if !approxEqual(afterDup.PaidAmount, otc.Total) {
		t.Errorf("idempotency broken: duplicate payment changed paid_amount to %v (was %v)",
			afterDup.PaidAmount, otc.Total)
	}
	if !approxEqual(afterDup.OutstandingAmount, 0) {
		t.Errorf("idempotency broken: outstanding went non-zero after duplicate: %v",
			afterDup.OutstandingAmount)
	}
	if afterDup.Status != "paid" {
		t.Errorf("status drifted after duplicate: %q", afterDup.Status)
	}
}

func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.01 // currency cents
}
