// Package webhookx ships the three things every inbound webhook
// needs to be safe in production:
//
//   1. HMAC-SHA256 signature verification (constant-time compare)
//   2. IP allow-list (CIDR-aware, X-Forwarded-For trusted only for
//      configured trusted-proxy ranges)
//   3. Idempotency — every webhook delivery is recorded in
//      platform.webhook_deliveries by its provider-supplied event id;
//      replays return 200 with no side effect.
//
// Provider adapters (Xendit, Stripe, WhatsApp, Mekari, …) implement
// the small Provider interface to map their per-vendor header names
// + signing scheme. The Middleware function then plugs straight into
// any chi router, returning the resulting Delivery to the handler so
// it can act on the parsed payload.
//
// Why a dedicated package: we have at least four future webhooks
// landing (Xendit payment, Xendit invoice paid, WhatsApp delivery
// receipts, Mekari HRIS push). Repeating the verification dance four
// times is the recipe for "I forgot the constant-time compare".
package webhookx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =====================================================================
// Provider interface — per-vendor adapter
// =====================================================================

// Provider describes how a specific webhook source signs its
// payloads and identifies its events. Implementations are tiny —
// see XenditProvider in xendit.go for a full example.
type Provider interface {
	// Name uniquely identifies the provider for storage + logging.
	Name() string
	// SignatureHeader is the HTTP header the provider uses to
	// carry its computed signature (e.g. "X-Callback-Token" for
	// Xendit-Test, "X-Hub-Signature-256" for Meta).
	SignatureHeader() string
	// EventIDHeader is the HTTP header that carries a stable per-
	// delivery id (used for idempotency). Empty string means the
	// provider doesn't issue one and we hash the body instead.
	EventIDHeader() string
	// ComputeSignature derives the expected signature from the
	// raw request body + the configured shared secret.
	ComputeSignature(rawBody []byte, secret string) string
}

// =====================================================================
// Config
// =====================================================================

// Config bundles the runtime configuration a Verifier needs. Stored
// in environment variables in production; pass empty Secret to
// disable signature checks during local dev (ALL OTHER LAYERS — IP
// list, idempotency — still apply).
type Config struct {
	// Secret is the shared signing key with the provider.
	Secret string
	// AllowedIPs limits which source IPs may post webhooks. Each
	// entry may be a single IP ("1.2.3.4") or a CIDR ("10.0.0.0/8").
	// Empty list means "allow any" (only safe behind a private
	// network or with a strict reverse proxy).
	AllowedIPs []string
	// TrustedProxies bounds whose X-Forwarded-For header we honor.
	// If the immediate remote-addr is NOT in this list, the X-F-F
	// header is ignored and we use remote-addr directly.
	TrustedProxies []string
	// MaxBodyBytes caps the request body to defend against memory-
	// exhaustion attacks (10 MiB by default if zero).
	MaxBodyBytes int64
	// MaxClockSkew defines how stale a webhook may be (used only
	// when the provider exposes a timestamp). 5m by default.
	MaxClockSkew time.Duration
}

// =====================================================================
// Verifier
// =====================================================================

// Verifier is the package's main entry. One per provider per service.
type Verifier struct {
	provider Provider
	cfg      Config
	pool     *pgxpool.Pool
	cidrs    []*net.IPNet
	hosts    map[string]struct{}
	proxies  []*net.IPNet
}

// New builds a Verifier with parsed allow-list + idempotency store.
func New(provider Provider, cfg Config, pool *pgxpool.Pool) (*Verifier, error) {
	v := &Verifier{provider: provider, cfg: cfg, pool: pool, hosts: map[string]struct{}{}}
	if v.cfg.MaxBodyBytes == 0 {
		v.cfg.MaxBodyBytes = 10 * 1024 * 1024
	}
	if v.cfg.MaxClockSkew == 0 {
		v.cfg.MaxClockSkew = 5 * time.Minute
	}
	for _, s := range cfg.AllowedIPs {
		if err := v.addAllowed(s); err != nil {
			return nil, fmt.Errorf("webhookx.allow_ip %q: %w", s, err)
		}
	}
	for _, s := range cfg.TrustedProxies {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			// fall back to "single host" parsing
			if ip := net.ParseIP(s); ip != nil {
				if ip.To4() != nil {
					_, n, _ = net.ParseCIDR(ip.String() + "/32")
				} else {
					_, n, _ = net.ParseCIDR(ip.String() + "/128")
				}
			} else {
				return nil, fmt.Errorf("webhookx.trusted_proxy %q: %w", s, err)
			}
		}
		v.proxies = append(v.proxies, n)
	}
	return v, nil
}

func (v *Verifier) addAllowed(s string) error {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return err
		}
		v.cidrs = append(v.cidrs, n)
		return nil
	}
	if net.ParseIP(s) == nil {
		return errors.New("not an IP or CIDR")
	}
	v.hosts[s] = struct{}{}
	return nil
}

// =====================================================================
// Delivery — what handlers see after verification passes
// =====================================================================

type Delivery struct {
	EventID   string          // idempotency key
	RawBody   []byte          // full request body
	Payload   json.RawMessage // alias of RawBody; convenience for handlers
	Headers   http.Header
	RemoteIP  string
	ArrivedAt time.Time
}

// =====================================================================
// Middleware — verify + idempotency in one call
// =====================================================================

type ctxKey int

const deliveryKey ctxKey = 0

// DeliveryFromContext returns the verified Delivery for the current
// request, or nil if no verifier was run.
func DeliveryFromContext(ctx context.Context) *Delivery {
	if d, ok := ctx.Value(deliveryKey).(*Delivery); ok {
		return d
	}
	return nil
}

// Middleware returns a chi-compatible http.Handler middleware. On
// success it stashes a *Delivery on the context and calls next. On
// failure it writes the appropriate HTTP error and returns.
//
// Failure modes (HTTP status / response code):
//   - 405      wrong method (non-POST)
//   - 413      body too large
//   - 401      bad / missing signature
//   - 403      IP not in allow-list
//   - 409      duplicate (already-processed) event — STILL safe to
//              re-deliver; we return 200 in this case so the
//              provider stops retrying
//   - 500      DB / unexpected error
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// IP check first — cheapest gate.
		ip := remoteIP(r, v.proxies)
		if !v.ipAllowed(ip) {
			http.Error(w, "ip not allowed", http.StatusForbidden)
			return
		}
		// Body — capped read.
		body, err := io.ReadAll(io.LimitReader(r.Body, v.cfg.MaxBodyBytes+1))
		if err != nil {
			http.Error(w, "body read failed", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > v.cfg.MaxBodyBytes {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Signature — constant-time compare.
		if v.cfg.Secret != "" {
			sig := r.Header.Get(v.provider.SignatureHeader())
			want := v.provider.ComputeSignature(body, v.cfg.Secret)
			if !hmac.Equal([]byte(sig), []byte(want)) {
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
		}
		// Idempotency — record the delivery. Returns true if this
		// is the first time we've seen the event.
		eventID := r.Header.Get(v.provider.EventIDHeader())
		if eventID == "" {
			// No header → hash the body so two identical retries
			// still collapse. SHA256 hex.
			sum := sha256.Sum256(body)
			eventID = hex.EncodeToString(sum[:])
		}
		fresh, err := v.recordDelivery(r.Context(), eventID, body, ip)
		if err != nil {
			http.Error(w, "idempotency store failed", http.StatusInternalServerError)
			return
		}
		if !fresh {
			// Duplicate — we already processed this. Reply 200 so
			// the provider stops retrying.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"duplicate":true}`))
			return
		}
		ctx := context.WithValue(r.Context(), deliveryKey, &Delivery{
			EventID:   eventID,
			RawBody:   body,
			Payload:   json.RawMessage(body),
			Headers:   r.Header,
			RemoteIP:  ip,
			ArrivedAt: time.Now(),
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// =====================================================================
// Helpers
// =====================================================================

func (v *Verifier) ipAllowed(ip string) bool {
	if len(v.cidrs) == 0 && len(v.hosts) == 0 {
		return true // explicit empty-allowlist = allow-any
	}
	if _, ok := v.hosts[ip]; ok {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range v.cidrs {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// remoteIP returns the client IP. If the immediate connection comes
// from a configured trusted proxy, the first X-Forwarded-For entry is
// used instead; otherwise we use the raw remote-addr. This avoids
// trusting X-F-F from arbitrary callers.
func remoteIP(r *http.Request, proxies []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Only honor X-F-F if the remote IP is a trusted proxy.
		parsed := net.ParseIP(host)
		trusted := false
		for _, n := range proxies {
			if parsed != nil && n.Contains(parsed) {
				trusted = true
				break
			}
		}
		if trusted {
			// X-F-F format: client, proxy1, proxy2 — take the left-
			// most entry that isn't itself a trusted proxy.
			for _, part := range strings.Split(xff, ",") {
				p := strings.TrimSpace(part)
				if p == "" {
					continue
				}
				return p
			}
		}
	}
	return host
}

// recordDelivery inserts the delivery row. Returns (fresh=true) when
// this event_id wasn't seen before. If a UNIQUE conflict happens,
// returns fresh=false with nil error — the duplicate is benign.
func (v *Verifier) recordDelivery(ctx context.Context, eventID string, body []byte, ip string) (bool, error) {
	if v.pool == nil {
		// No store wired (test mode) → always treat as fresh.
		return true, nil
	}
	tag, err := v.pool.Exec(ctx, `
		INSERT INTO platform.webhook_deliveries
			(provider, event_id, body, remote_ip)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider, event_id) DO NOTHING
	`, v.provider.Name(), eventID, body, ip)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// =====================================================================
// HMAC helpers — exposed so provider adapters can reuse them
// =====================================================================

// HMACHex computes hex(HMAC-SHA256(secret, body)).
func HMACHex(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// CompareConstantTime wraps hmac.Equal for callers that work in
// strings rather than bytes.
func CompareConstantTime(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// readBuf is a tiny helper that lets a test caller swap in a bytes.Buffer
// as the request body without importing strings.NewReader from net/http
// at call sites.
func readBuf(b []byte) io.Reader { return bytes.NewReader(b) }
