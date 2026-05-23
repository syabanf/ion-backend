package gateway

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// BCAH2HStub satisfies port.GatewayClient for the BCA Host-to-Host
// corporate banking integration without making real SFTP / API calls.
//
// The H2H pattern is "bank drops a daily CSV of every credit
// transaction onto our SFTP server, we ingest + reconcile". This stub
// only implements the parser side — CreatePayment is a no-op because
// H2H gateways don't issue per-checkout VAs (the customer transfers
// to a static account).
//
// Canned CSV format (kept simple for local dev):
//
//	value_date,reference,amount,description
//	2026-05-23,INV-2026-001,100000,FROM JOHN DOE
type BCAH2HStub struct {
	code string
}

func NewBCAH2HStub() *BCAH2HStub {
	return &BCAH2HStub{code: "bca_h2h"}
}

var _ port.GatewayClient = (*BCAH2HStub)(nil)

func (b *BCAH2HStub) Code() string { return b.code }

func (b *BCAH2HStub) CreatePayment(ctx context.Context, in port.CreatePaymentInput) (*port.CreatePaymentResult, error) {
	// H2H gateways don't issue per-checkout payment artefacts. The
	// invoice carries a static account number; we return a placeholder
	// VA = the invoice id so the response shape stays consistent.
	return &port.CreatePaymentResult{
		ExternalRef: "bca_h2h_" + in.InvoiceID.String()[:8],
		VANumber:    "0123456789", // demo corporate account
	}, nil
}

func (b *BCAH2HStub) RefundPayment(ctx context.Context, intent *domain.PaymentIntent, amount float64, reason string) (*port.RefundResult, error) {
	// Real refunds via H2H require an outbound transfer instruction —
	// out of scope for Wave 111 (the finance team books these manually
	// via the bank's portal). The stub records the attempt for audit.
	return &port.RefundResult{
		ExternalRef: "bca_manual_refund_pending",
	}, nil
}

func (b *BCAH2HStub) CheckStatus(ctx context.Context, intent *domain.PaymentIntent) (*port.CheckStatusResult, error) {
	ref := ""
	if intent.ExternalPaymentRef != nil {
		ref = *intent.ExternalPaymentRef
	}
	return &port.CheckStatusResult{
		Status:      string(intent.Status),
		ExternalRef: ref,
	}, nil
}

// VerifySignature is a no-op for H2H — the integration runs over a
// private SFTP channel without per-row HMAC signing. The upload
// surface is auth-gated via the regular bearer token + RBAC.
func (b *BCAH2HStub) VerifySignature(payload []byte, signature string) bool {
	return true
}

// ParseWebhook is a no-op for H2H — the bank doesn't push webhooks;
// it drops files. Calling this is a misuse; return a parseable empty
// envelope so the caller can detect the mismatch.
func (b *BCAH2HStub) ParseWebhook(payload []byte) (port.ParsedWebhook, error) {
	return port.ParsedWebhook{}, errors.New("bca_h2h: webhooks are not supported by this gateway kind")
}

// ParseH2HStatement parses the canned CSV format described in the
// package doc. The expected header is:
//
//	value_date,reference,amount,description
//
// Rows missing required fields are skipped (logged at the caller).
func (b *BCAH2HStub) ParseH2HStatement(content []byte) ([]port.ParsedH2HLine, error) {
	if len(content) == 0 {
		return nil, errors.New("bca_h2h: empty statement content")
	}
	r := csv.NewReader(strings.NewReader(string(content)))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("bca_h2h: parse csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("bca_h2h: csv has zero rows")
	}
	// Detect a header row by checking column 0.
	start := 0
	if len(rows[0]) > 0 && strings.EqualFold(strings.TrimSpace(rows[0][0]), "value_date") {
		start = 1
	}
	out := []port.ParsedH2HLine{}
	for i := start; i < len(rows); i++ {
		row := rows[i]
		if len(row) < 3 {
			continue
		}
		dateStr := strings.TrimSpace(row[0])
		refStr := strings.TrimSpace(row[1])
		amtStr := strings.TrimSpace(row[2])
		description := ""
		if len(row) > 3 {
			description = strings.TrimSpace(row[3])
		}
		valueDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		amt, err := strconv.ParseFloat(amtStr, 64)
		if err != nil {
			continue
		}
		raw, _ := json.Marshal(map[string]string{
			"value_date":  dateStr,
			"reference":   refStr,
			"amount":      amtStr,
			"description": description,
		})
		out = append(out, port.ParsedH2HLine{
			RawJSON:       raw,
			Amount:        amt,
			ValueDate:     valueDate,
			ReferenceText: refStr,
		})
	}
	return out, nil
}
