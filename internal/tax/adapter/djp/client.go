// Wave 101 — DJP e-Faktur real-client scaffolding.
//
// Wave 93 shipped a stub gateway that always returned 503 with
// `djp.scaffold`. This file ships the production-shaped client: an
// HTTP-based DJPGateway that signs + posts JSON to the DJP API. The
// real DJP credentials don't land in this wave — we read the toggle
// from the environment and short-circuit to the same 503 behavior
// when DJP_ENABLED is not "true", so the binary keeps starting
// cleanly in dev / staging.
//
// Swap matrix:
//
//	DJP_ENABLED=true   → real HTTP calls (POST/GET against DJP_BASE_URL)
//	DJP_ENABLED unset  → 503 djp.scaffold (same as the stub)
//	DJP_ENABLED=false  → 503 djp.scaffold (explicit opt-out)
//
// Wiring this in cmd/enterprise-svc/main.go is a single line — the
// constructor returns whichever behavior matches the env.
package djp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Config drives the DJP HTTP client. All four fields are sourced from
// the environment via ConfigFromEnv; tests can construct one directly.
type Config struct {
	// Enabled is the master toggle. When false, the gateway returns the
	// same 503 djp.scaffold response as the Wave 93 stub — no HTTP
	// traffic is generated. This lets ops flip the integration on/off
	// without redeploying.
	Enabled bool

	// BaseURL is the DJP API root (no trailing slash). Empty when
	// Enabled is false.
	BaseURL string

	// APIKey is the bearer token sent in the Authorization header.
	// Sourced from DJP_API_KEY; secret — never logged.
	APIKey string

	// Timeout caps each individual HTTP call. Defaults to 30s if zero.
	Timeout time.Duration

	// Logger is optional; we never log the API key or full payloads
	// (would risk PII), but boundary events (request started, status,
	// error class) go through here at Info/Warn.
	Logger *slog.Logger
}

// ConfigFromEnv reads Config from process environment. Empty values
// flow through; the constructor decides what to do with them based on
// Enabled.
func ConfigFromEnv() Config {
	return Config{
		Enabled: strings.EqualFold(os.Getenv("DJP_ENABLED"), "true"),
		BaseURL: strings.TrimRight(os.Getenv("DJP_BASE_URL"), "/"),
		APIKey:  os.Getenv("DJP_API_KEY"),
		Timeout: 30 * time.Second,
	}
}

// Client is the HTTP-based DJPGateway. Constructed via NewClient,
// which inspects cfg.Enabled and returns either the real HTTP client
// or a stub-mode client that short-circuits to 503.
type Client struct {
	cfg    Config
	http   *http.Client
	logger *slog.Logger
}

// NewClient constructs the gateway. Always non-nil; the Enabled flag
// flows through to the methods so the same struct handles both modes.
// The caller (cmd/enterprise-svc/main.go) logs at startup which mode
// is active so an operator can verify the toggle took effect.
func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "tax.djp.client")
	if cfg.Enabled {
		logger.Info("DJP gateway mode=real", "base_url", cfg.BaseURL)
	} else {
		logger.Info("DJP gateway mode=stub", "reason", "DJP_ENABLED != true")
	}
	return &Client{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.Timeout},
		logger: logger,
	}
}

// Compile-time check the Client satisfies the port contract.
var _ port.DJPGateway = (*Client)(nil)

// scaffoldErr is the canonical 503 returned when DJP_ENABLED != true.
// Stable error code keeps the FE flag-detection logic identical to
// the Wave 93 stub.
func scaffoldErr() error {
	return derrors.New(
		derrors.KindUnavailable,
		"djp.scaffold",
		"DJP integration not enabled (set DJP_ENABLED=true to activate)",
	)
}

// issueRequest is the JSON body POSTed to /api/faktur/issue. Field
// names match the DJP e-Faktur convention so the real endpoint can
// consume it without a mapping layer.
type issueRequest struct {
	InvoiceID       string  `json:"invoice_id"`
	SubsidiaryID    string  `json:"subsidiary_id,omitempty"`
	JenisFaktur     string  `json:"jenis_faktur"`
	NPWPLawan       string  `json:"npwp_lawan_transaksi"`
	DPP             float64 `json:"dpp"`
	PPN             float64 `json:"ppn"`
	TaxSnapshotHash string  `json:"tax_snapshot_hash,omitempty"`
}

// issueResponse is the slim shape we care about. Real DJP payload
// carries far more fields — we persist the full body byte-for-byte
// via FakturPajak.DJPResponsePayload (jsonb) and only decode the
// nomor_seri + DPP here.
type issueResponse struct {
	NomorSeri  string  `json:"nomor_seri"`
	DPPDecoded float64 `json:"dpp_decoded"`
	Status     string  `json:"status"`
}

// IssueFaktur posts the draft faktur to DJP and returns the issued
// serial + raw payload. Short-circuits with scaffoldErr when Enabled
// is false (matches the stub).
func (c *Client) IssueFaktur(ctx context.Context, f *domain.FakturPajak) (string, []byte, error) {
	if !c.cfg.Enabled {
		return "", nil, scaffoldErr()
	}
	subsidiary := ""
	if f.SubsidiaryID != nil {
		subsidiary = f.SubsidiaryID.String()
	}
	snap := ""
	if f.TaxSnapshotHash != nil {
		snap = *f.TaxSnapshotHash
	}
	body, err := json.Marshal(issueRequest{
		InvoiceID:       f.InvoiceID.String(),
		SubsidiaryID:    subsidiary,
		JenisFaktur:     string(f.JenisFaktur),
		NPWPLawan:       f.NPWPLawanTransaksi,
		DPP:             f.DPP,
		PPN:             f.PPN,
		TaxSnapshotHash: snap,
	})
	if err != nil {
		return "", nil, derrors.Wrap(derrors.KindInternal,
			"djp.marshal", "marshal issue request", err)
	}
	url := c.cfg.BaseURL + "/api/faktur/issue"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", nil, derrors.Wrap(derrors.KindInternal,
			"djp.build_request", "build issue request", err)
	}
	c.setStandardHeaders(req)
	c.logger.Info("djp.issue request", "url", url, "faktur_id", f.ID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, derrors.Wrap(derrors.KindUnavailable,
			"djp.transport", "DJP issue transport error", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logger.Warn("djp.issue non-2xx",
			"status", resp.StatusCode, "faktur_id", f.ID.String())
		return "", payload, derrors.New(derrors.KindUnavailable,
			"djp.issue_failed",
			fmt.Sprintf("DJP returned HTTP %d", resp.StatusCode))
	}
	var out issueResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", payload, derrors.Wrap(derrors.KindInternal,
			"djp.parse_response", "parse DJP issue response", err)
	}
	if out.NomorSeri == "" {
		return "", payload, derrors.New(derrors.KindUnavailable,
			"djp.empty_serial", "DJP returned empty nomor_seri")
	}
	c.logger.Info("djp.issue success",
		"faktur_id", f.ID.String(), "nomor_seri", out.NomorSeri)
	return out.NomorSeri, payload, nil
}

// CheckStatus polls DJP for a previously-issued faktur. Mirrors
// IssueFaktur's short-circuit + error mapping.
func (c *Client) CheckStatus(ctx context.Context, nomorSeri string) (string, []byte, error) {
	if !c.cfg.Enabled {
		return "", nil, scaffoldErr()
	}
	if strings.TrimSpace(nomorSeri) == "" {
		return "", nil, derrors.Validation(
			"djp.nomor_seri_required",
			"nomor_seri is required to check DJP status",
		)
	}
	url := fmt.Sprintf("%s/api/faktur/%s/status", c.cfg.BaseURL, nomorSeri)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, derrors.Wrap(derrors.KindInternal,
			"djp.build_request", "build status request", err)
	}
	c.setStandardHeaders(req)
	c.logger.Info("djp.status request", "url", url, "nomor_seri", nomorSeri)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, derrors.Wrap(derrors.KindUnavailable,
			"djp.transport", "DJP status transport error", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", payload, derrors.New(derrors.KindUnavailable,
			"djp.status_failed",
			fmt.Sprintf("DJP returned HTTP %d", resp.StatusCode))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", payload, derrors.Wrap(derrors.KindInternal,
			"djp.parse_response", "parse DJP status response", err)
	}
	return out.Status, payload, nil
}

// setStandardHeaders applies the auth + content-type used on every
// outbound request.
func (c *Client) setStandardHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ion-core/tax-svc")
}
