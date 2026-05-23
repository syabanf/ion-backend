package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 111 — PaymentIntent state-machine contract tests.
//
// Transitions:
//   created → routing → pending → succeeded
//                             ↘ failed
//                             ↘ expired
//                             ↘ cancelled (from created / routing / pending)
//   succeeded → partially_refunded → refunded
// =====================================================================

func newIntentAt(t *testing.T, status PaymentStatus) *PaymentIntent {
	t.Helper()
	i, err := NewPaymentIntent(uuid.New(), 100000, "idem-"+t.Name())
	if err != nil {
		t.Fatalf("NewPaymentIntent: %v", err)
	}
	switch status {
	case PaymentStatusCreated:
	case PaymentStatusRouting:
		if err := i.Route(uuid.New(), RouteDecision{ChosenGatewayID: uuid.New()}); err != nil {
			t.Fatalf("Route: %v", err)
		}
	case PaymentStatusPending:
		if err := i.Route(uuid.New(), RouteDecision{ChosenGatewayID: uuid.New()}); err != nil {
			t.Fatalf("Route: %v", err)
		}
		if err := i.MarkPending("ext-ref-123"); err != nil {
			t.Fatalf("MarkPending: %v", err)
		}
	case PaymentStatusSucceeded:
		if err := i.Route(uuid.New(), RouteDecision{ChosenGatewayID: uuid.New()}); err != nil {
			t.Fatalf("Route: %v", err)
		}
		if err := i.MarkPending("ext-ref-123"); err != nil {
			t.Fatalf("MarkPending: %v", err)
		}
		if err := i.MarkSucceeded(time.Now()); err != nil {
			t.Fatalf("MarkSucceeded: %v", err)
		}
	case PaymentStatusPartiallyRefunded:
		if err := i.Route(uuid.New(), RouteDecision{ChosenGatewayID: uuid.New()}); err != nil {
			t.Fatalf("Route: %v", err)
		}
		if err := i.MarkPending("ext-ref-123"); err != nil {
			t.Fatalf("MarkPending: %v", err)
		}
		if err := i.MarkSucceeded(time.Now()); err != nil {
			t.Fatalf("MarkSucceeded: %v", err)
		}
		if err := i.MarkPartiallyRefunded(40000); err != nil {
			t.Fatalf("MarkPartiallyRefunded: %v", err)
		}
	}
	return i
}

func TestPaymentIntentSM_HappyPath(t *testing.T) {
	i, err := NewPaymentIntent(uuid.New(), 100000, "idem-happy")
	if err != nil {
		t.Fatalf("NewPaymentIntent: %v", err)
	}
	if i.Status != PaymentStatusCreated {
		t.Fatalf("expected created, got %q", i.Status)
	}
	if err := i.Route(uuid.New(), RouteDecision{ChosenGatewayCode: "xendit"}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if i.Status != PaymentStatusRouting {
		t.Fatalf("expected routing, got %q", i.Status)
	}
	if err := i.MarkPending("8800123456"); err != nil {
		t.Fatalf("MarkPending: %v", err)
	}
	if i.ExternalPaymentRef == nil || *i.ExternalPaymentRef != "8800123456" {
		t.Fatalf("external ref not stored")
	}
	if err := i.MarkSucceeded(time.Now()); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if i.PaidAt == nil {
		t.Fatalf("PaidAt not set on success")
	}
}

func TestPaymentIntentSM_RefundFlow(t *testing.T) {
	i := newIntentAt(t, PaymentStatusSucceeded)
	if err := i.MarkPartiallyRefunded(30000); err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	if i.Status != PaymentStatusPartiallyRefunded {
		t.Fatalf("expected partially_refunded, got %q", i.Status)
	}
	if i.RefundedAmount != 30000 {
		t.Fatalf("RefundedAmount = %v want 30000", i.RefundedAmount)
	}
	// Bump partial refund total.
	if err := i.MarkPartiallyRefunded(70000); err != nil {
		t.Fatalf("partial refund #2: %v", err)
	}
	// Full refund.
	if err := i.MarkFullyRefunded(); err != nil {
		t.Fatalf("full refund: %v", err)
	}
	if i.Status != PaymentStatusRefunded {
		t.Fatalf("expected refunded, got %q", i.Status)
	}
	if i.RefundedAmount != i.Amount {
		t.Fatalf("RefundedAmount not equal to Amount on full refund")
	}
}

func TestPaymentIntentSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     PaymentStatus
		action   func(*PaymentIntent) error
		wantCode string
	}{
		{"created -> succeeded (skip route+pending)", PaymentStatusCreated,
			func(i *PaymentIntent) error { return i.MarkSucceeded(time.Now()) },
			"intent.cannot_mark_succeeded"},
		{"created -> pending (skip route)", PaymentStatusCreated,
			func(i *PaymentIntent) error { return i.MarkPending("ext") },
			"intent.cannot_mark_pending"},
		{"routing -> succeeded (skip pending)", PaymentStatusRouting,
			func(i *PaymentIntent) error { return i.MarkSucceeded(time.Now()) },
			"intent.cannot_mark_succeeded"},
		{"succeeded -> cancel (post-success cancel blocked)", PaymentStatusSucceeded,
			func(i *PaymentIntent) error { return i.MarkCancelled() },
			"intent.cannot_cancel"},
		{"succeeded -> pending (backward)", PaymentStatusSucceeded,
			func(i *PaymentIntent) error { return i.MarkPending("ext") },
			"intent.cannot_mark_pending"},
		{"refund > amount", PaymentStatusSucceeded,
			func(i *PaymentIntent) error { return i.MarkPartiallyRefunded(200000) },
			"intent.refund_exceeds_amount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := newIntentAt(t, tc.from)
			err := tc.action(i)
			if err == nil {
				t.Fatalf("expected error, status = %q", i.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("error not *derrors.Error: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q want %q", de.Code, tc.wantCode)
			}
		})
	}
}

func TestPaymentIntentSM_CancelFromMultipleStates(t *testing.T) {
	for _, s := range []PaymentStatus{PaymentStatusCreated, PaymentStatusRouting, PaymentStatusPending} {
		t.Run(string(s), func(t *testing.T) {
			i := newIntentAt(t, s)
			if err := i.MarkCancelled(); err != nil {
				t.Fatalf("MarkCancelled: %v", err)
			}
			if i.Status != PaymentStatusCancelled {
				t.Fatalf("expected cancelled, got %q", i.Status)
			}
			if i.CancelledAt == nil {
				t.Fatalf("CancelledAt not set")
			}
		})
	}
}

func TestPaymentIntent_MatchesAmount(t *testing.T) {
	min := 10000.0
	max := 1000000.0
	g := &PaymentGateway{MinAmount: &min, MaxAmount: &max}
	cases := []struct {
		amount float64
		want   bool
	}{
		{5000, false},
		{10000, true},
		{500000, true},
		{1000000, true},
		{1500000, false},
	}
	for _, c := range cases {
		if got := g.MatchesAmount(c.amount); got != c.want {
			t.Errorf("MatchesAmount(%v) = %v want %v", c.amount, got, c.want)
		}
	}
	// nil bounds = unconstrained
	g2 := &PaymentGateway{}
	if !g2.MatchesAmount(99999999) {
		t.Errorf("nil bounds should accept everything")
	}
}

func TestRefundSM_HappyPath(t *testing.T) {
	by := uuid.New()
	r, err := NewRefund(uuid.New(), 50000, "duplicate payment", &by)
	if err != nil {
		t.Fatalf("NewRefund: %v", err)
	}
	if r.Status != RefundStatusRequested {
		t.Fatalf("status = %q want requested", r.Status)
	}
	if err := r.Approve(uuid.New()); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := r.StartProcessing(); err != nil {
		t.Fatalf("StartProcessing: %v", err)
	}
	if err := r.MarkCompleted("xnd-ref-99"); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	if r.Status != RefundStatusCompleted {
		t.Fatalf("status = %q want completed", r.Status)
	}
	if r.ExternalRefundRef == nil || *r.ExternalRefundRef != "xnd-ref-99" {
		t.Fatalf("external ref not stored")
	}
}

func TestRefundSM_RejectAndFail(t *testing.T) {
	by := uuid.New()
	r, err := NewRefund(uuid.New(), 50000, "test", &by)
	if err != nil {
		t.Fatalf("NewRefund: %v", err)
	}
	if err := r.Reject("not eligible"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if r.Status != RefundStatusRejected {
		t.Fatalf("status = %q want rejected", r.Status)
	}
	if err := r.Approve(uuid.New()); err == nil {
		t.Fatalf("Approve after reject should fail")
	}

	// Fail path: requested → approved → processing → failed.
	r2, _ := NewRefund(uuid.New(), 50000, "test", &by)
	_ = r2.Approve(uuid.New())
	_ = r2.StartProcessing()
	if err := r2.MarkFailed("gateway timeout"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if r2.Status != RefundStatusFailed {
		t.Fatalf("status = %q want failed", r2.Status)
	}
}

func TestH2HMatchByReference(t *testing.T) {
	paidAt := time.Now()
	cases := []struct {
		name          string
		lineRef       string
		lineAmount    float64
		lineValueDate time.Time
		intentRef     string
		intentAmount  float64
		intentPaidAt  *time.Time
		wantMethod    string
		wantMinConf   float64
	}{
		{"exact reference", "INV-2026-001", 100000, paidAt, "INV-2026-001", 100000, &paidAt, "reference_exact", 0.9},
		{"substring with amount", "PAYMENT FOR INV-2026-001 OK", 100000, paidAt, "INV-2026-001", 100000, &paidAt, "reference_substring_amount", 0.8},
		{"amount + date window", "irrelevant noise", 100000, paidAt, "INV-2026-001", 100000, &paidAt, "amount_date_window", 0.4},
		{"amount but date too far", "irrelevant", 100000, paidAt.Add(7 * 24 * time.Hour), "INV-2026-001", 100000, &paidAt, "no_match", 0},
		{"no match at all", "garbage", 999, paidAt, "INV-XYZ", 100000, &paidAt, "no_match", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conf, method := MatchByReference(
				c.lineRef, c.lineAmount, c.lineValueDate,
				c.intentRef, c.intentAmount, c.intentPaidAt,
			)
			if method != c.wantMethod {
				t.Errorf("method = %q want %q", method, c.wantMethod)
			}
			if conf < c.wantMinConf {
				t.Errorf("confidence = %v want >= %v", conf, c.wantMinConf)
			}
		})
	}
}

func TestVerifySignatureRoundTrip(t *testing.T) {
	payload := []byte(`{"event":"payment.paid","amount":100000}`)
	secret := "supersecret-32-chars-aaaaaaaaaaaaaa"
	if VerifySignature(payload, "deadbeef", secret) {
		t.Fatalf("bogus signature should not verify")
	}
	// Recompute the expected signature with the standard lib so we can
	// round-trip through VerifySignature.
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	good := hex.EncodeToString(mac.Sum(nil))
	if !VerifySignature(payload, good, secret) {
		t.Fatalf("recomputed signature should verify")
	}
}

func TestWebhookSM_HappyPath(t *testing.T) {
	w, err := NewPaymentWebhook(uuid.New(), "evt-001", []byte(`{}`))
	if err != nil {
		t.Fatalf("NewPaymentWebhook: %v", err)
	}
	if w.Status != WebhookStatusReceived {
		t.Fatalf("status = %q want received", w.Status)
	}
	if err := w.MarkVerified(); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	intentID := uuid.New()
	if err := w.MarkProcessed(&intentID); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if w.Status != WebhookStatusProcessed {
		t.Fatalf("status = %q want processed", w.Status)
	}
	if w.PaymentIntentID == nil || *w.PaymentIntentID != intentID {
		t.Fatalf("intent id not propagated")
	}
}

func TestH2HStatementSM(t *testing.T) {
	s, err := NewH2HBankStatement(uuid.New(), "bca-2026-05-23.csv", []byte("date,ref,amount\n2026-05-23,INV-001,100000"))
	if err != nil {
		t.Fatalf("NewH2HBankStatement: %v", err)
	}
	if s.Status != H2HStatementStatusParsing {
		t.Fatalf("status = %q want parsing", s.Status)
	}
	if err := s.MarkParsed(1); err != nil {
		t.Fatalf("MarkParsed: %v", err)
	}
	if err := s.StartMatching(); err != nil {
		t.Fatalf("StartMatching: %v", err)
	}
	if err := s.CompleteMatching(1, 0); err != nil {
		t.Fatalf("CompleteMatching: %v", err)
	}
	if s.Status != H2HStatementStatusMatched {
		t.Fatalf("expected matched (no unmatched lines), got %q", s.Status)
	}
	// Re-run matching after new intents land.
	if err := s.StartMatching(); err != nil {
		t.Fatalf("StartMatching re-run: %v", err)
	}
	if err := s.CompleteMatching(2, 1); err != nil {
		t.Fatalf("CompleteMatching re-run: %v", err)
	}
	if s.Status != H2HStatementStatusPartial {
		t.Fatalf("expected partial (one unmatched), got %q", s.Status)
	}
}
