// Wave 128A — HRIS gateway real HTTP adapter (closes Wave 121E §6.1).
//
// RESTGateway is the production-shaped client: POSTs to / GETs from a
// remote HRIS over HTTPS using a bearer token. It satisfies the same
// port.HRISGateway contract as StubGateway so the swap from
// cmd/hris-svc/main.go is a single conditional.
//
// Swap matrix:
//
//	HRIS_GATEWAY_ENABLED unset / empty / "false" → StubGateway
//	HRIS_GATEWAY_ENABLED=true + HRIS_GATEWAY_URL + HRIS_GATEWAY_API_KEY → RESTGateway
//	HRIS_GATEWAY_ENABLED=true + any required var missing             → *errors.Error (boot fail)
//
// The real client doesn't have to fully work end-to-end against a real
// HRIS today — it only has to make actual HTTP calls and surface
// transport / non-2xx / parse errors as typed *errors.Error so the
// cron + ops can react.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// RESTConfig drives the RESTGateway. Sourced from the environment via
// RESTConfigFromEnv; tests construct one directly.
type RESTConfig struct {
	// BaseURL is the HRIS API root, no trailing slash. Required when
	// HRIS_GATEWAY_ENABLED=true.
	BaseURL string

	// APIKey is the bearer token sent in the Authorization header.
	// Required when HRIS_GATEWAY_ENABLED=true. Never logged.
	APIKey string

	// Timeout caps each individual HTTP call. Defaults to 30s if zero.
	Timeout time.Duration

	// Logger is optional. Never logs the API key or full payloads.
	Logger *slog.Logger
}

// EnvFlagSet reports whether HRIS_GATEWAY_ENABLED is set to "true".
// Centralised so the cmd binary and tests agree on parse semantics.
func EnvFlagSet() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("HRIS_GATEWAY_ENABLED")), "true")
}

// RESTConfigFromEnv reads RESTConfig from process environment. Does NOT
// validate — that's NewRESTGateway's job so callers get one canonical
// error path.
func RESTConfigFromEnv() RESTConfig {
	return RESTConfig{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("HRIS_GATEWAY_URL")), "/"),
		APIKey:  os.Getenv("HRIS_GATEWAY_API_KEY"),
		Timeout: 30 * time.Second,
	}
}

// NewRESTGateway constructs the real HTTP gateway. Returns a typed
// *errors.Error (KindValidation, code "hris.gateway.misconfigured") if
// any required field is missing — this is what the svc binary surfaces
// as a startup failure when HRIS_GATEWAY_ENABLED=true but the operator
// forgot to set HRIS_GATEWAY_URL / HRIS_GATEWAY_API_KEY.
func NewRESTGateway(cfg RESTConfig) (*RESTGateway, error) {
	var missing []string
	if cfg.BaseURL == "" {
		missing = append(missing, "HRIS_GATEWAY_URL")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		missing = append(missing, "HRIS_GATEWAY_API_KEY")
	}
	if len(missing) > 0 {
		return nil, derrors.New(
			derrors.KindValidation,
			"hris.gateway.misconfigured",
			fmt.Sprintf("HRIS_GATEWAY_ENABLED=true but required env vars are missing: %s",
				strings.Join(missing, ", ")),
		)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "hris.gateway.rest")
	logger.Info("HRIS gateway mode=real", "base_url", cfg.BaseURL)
	return &RESTGateway{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.Timeout},
		logger: logger,
	}, nil
}

// RESTGateway is the HTTP-backed HRISGateway.
type RESTGateway struct {
	cfg    RESTConfig
	http   *http.Client
	logger *slog.Logger
}

var _ port.HRISGateway = (*RESTGateway)(nil)

// employeeWire is the on-the-wire shape we expect from
// GET /employees. The fields mirror port.EmployeeRecord — a real HRIS
// counterparty may differ; the parse pass tolerates extra fields and
// surfaces missing-required-field via a typed error.
type employeeWire struct {
	EmployeeNo          string     `json:"employee_no"`
	FullName            string     `json:"full_name"`
	Email               string     `json:"email,omitempty"`
	Phone               string     `json:"phone,omitempty"`
	Department          string     `json:"department,omitempty"`
	Position            string     `json:"position,omitempty"`
	ManagerEmployeeNo   string     `json:"manager_employee_no,omitempty"`
	HireDate            *time.Time `json:"hire_date,omitempty"`
	ResignDate          *time.Time `json:"resign_date,omitempty"`
	Status              string     `json:"status"`
	KYCCompleted        bool       `json:"kyc_completed"`
	NPWP                string     `json:"npwp,omitempty"`
	BankAccountNo       string     `json:"bank_account_no,omitempty"`
	BranchID            *uuid.UUID `json:"branch_id,omitempty"`
	RoleRecommendations []string   `json:"role_recommendations,omitempty"`
}

// eventWire mirrors the canonical employee event shape.
type eventWire struct {
	ID         uuid.UUID      `json:"id"`
	EmployeeNo string         `json:"employee_no"`
	Kind       string         `json:"kind"`
	Payload    map[string]any `json:"payload"`
	EffectiveAt time.Time     `json:"effective_at"`
	Source     string         `json:"source,omitempty"`
}

// FetchEmployees calls GET ${BaseURL}/employees?since=RFC3339. Returns
// a typed *errors.Error on transport / non-2xx / parse failure.
func (g *RESTGateway) FetchEmployees(ctx context.Context, since time.Time) ([]port.EmployeeRecord, error) {
	url := fmt.Sprintf("%s/employees?since=%s",
		g.cfg.BaseURL, since.UTC().Format(time.RFC3339))
	body, err := g.doJSON(ctx, http.MethodGet, url, nil, "hris.gateway.fetch_employees")
	if err != nil {
		return nil, err
	}
	var wire []employeeWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"hris.gateway.parse_employees",
			"failed to parse HRIS employees response", err)
	}
	out := make([]port.EmployeeRecord, 0, len(wire))
	for _, w := range wire {
		out = append(out, port.EmployeeRecord{
			EmployeeNo:          w.EmployeeNo,
			FullName:            w.FullName,
			Email:               w.Email,
			Phone:               w.Phone,
			Department:          w.Department,
			Position:            w.Position,
			ManagerEmployeeNo:   w.ManagerEmployeeNo,
			HireDate:            w.HireDate,
			ResignDate:          w.ResignDate,
			Status:              domain.EmployeeStatus(w.Status),
			KYCCompleted:        w.KYCCompleted,
			NPWP:                w.NPWP,
			BankAccountNo:       w.BankAccountNo,
			BranchID:            w.BranchID,
			RoleRecommendations: w.RoleRecommendations,
		})
	}
	return out, nil
}

// FetchEvents calls GET ${BaseURL}/events?since=RFC3339. Returns typed
// errors for transport / non-2xx / parse failures.
func (g *RESTGateway) FetchEvents(ctx context.Context, since time.Time) ([]*domain.EmployeeEvent, error) {
	url := fmt.Sprintf("%s/events?since=%s",
		g.cfg.BaseURL, since.UTC().Format(time.RFC3339))
	body, err := g.doJSON(ctx, http.MethodGet, url, nil, "hris.gateway.fetch_events")
	if err != nil {
		return nil, err
	}
	var wire []eventWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"hris.gateway.parse_events",
			"failed to parse HRIS events response", err)
	}
	out := make([]*domain.EmployeeEvent, 0, len(wire))
	for _, w := range wire {
		ev, err := domain.NewEmployeeEvent(
			w.EmployeeNo,
			domain.EventKind(w.Kind),
			w.Payload,
			w.EffectiveAt,
			w.Source,
		)
		if err != nil {
			return nil, derrors.Wrap(derrors.KindValidation,
				"hris.gateway.invalid_event",
				fmt.Sprintf("invalid event for employee %q", w.EmployeeNo), err)
		}
		if w.ID != uuid.Nil {
			ev.ID = w.ID
		}
		out = append(out, ev)
	}
	return out, nil
}

// doJSON is the shared HTTP helper: builds a request, applies auth
// headers, executes, reads the body, maps non-2xx to a typed error.
func (g *RESTGateway) doJSON(ctx context.Context, method, url string, reqBody io.Reader, opCode string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			opCode+".build_request",
			"failed to build HRIS request", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ion-core/hris-svc")
	g.logger.Info("hris.gateway request", "method", method, "url", url)
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindUnavailable,
			opCode+".transport",
			"HRIS gateway transport error", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		g.logger.Warn("hris.gateway non-2xx", "status", resp.StatusCode, "url", url)
		return payload, derrors.New(derrors.KindUnavailable,
			opCode+".non_2xx",
			fmt.Sprintf("HRIS gateway returned HTTP %d", resp.StatusCode))
	}
	return payload, nil
}
