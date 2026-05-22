package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
)

// RequestLogger emits one structured log line per request, with status, latency,
// method, path, and request_id.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				log.Info("http_request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"latency_ms", time.Since(start).Milliseconds(),
					"request_id", chimw.GetReqID(r.Context()),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

type ctxKey int

const (
	ctxKeyClaims ctxKey = iota
)

// RequireAuth verifies the Authorization: Bearer header and attaches Claims
// to the request context. Returns 401 if invalid or missing.
func RequireAuth(v *auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				WriteError(w, errors.Unauthorized("auth.missing", "missing bearer token"))
				return
			}
			claims, err := v.Verify(strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				WriteError(w, errors.Unauthorized("auth.invalid", "invalid or expired token"))
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext extracts the verified claims attached by RequireAuth.
// Returns nil if no claims are present.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	v, _ := ctx.Value(ctxKeyClaims).(*auth.Claims)
	return v
}

// RequireRole returns 403 unless the authenticated user has at least one
// of the required role names. Must be chained after RequireAuth.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allow := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allow[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := ClaimsFromContext(r.Context())
			if c == nil {
				WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
				return
			}
			for _, role := range c.Roles {
				if _, ok := allow[role]; ok {
					next.ServeHTTP(w, r)
					return
				}
			}
			WriteError(w, errors.Forbidden("auth.role", "insufficient privileges"))
		})
	}
}

// RequirePermission returns 403 unless the authenticated user has at least
// one of the listed permission keys. Must be chained after RequireAuth.
//
// This is the preferred authorization primitive — finer-grained than role,
// and works without any context-specific knowledge in the middleware. Each
// service's handlers declare which permission key they need; identity-svc
// is the only source of truth for which permissions a user holds (encoded
// in the JWT at issuance).
func RequirePermission(keys ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := ClaimsFromContext(r.Context())
			if c == nil {
				WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
				return
			}
			if c.HasAnyPermission(keys...) {
				next.ServeHTTP(w, r)
				return
			}
			WriteError(w, errors.Forbidden("auth.permission", "insufficient privileges"))
		})
	}
}

// --- JSON helpers ---

// WriteJSON writes a successful JSON response.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errorResponse is the wire format for error payloads.
type errorResponse struct {
	Error struct {
		Code              string `json:"code"`
		Kind              string `json:"kind"`
		Message           string `json:"message"`
		TraceID           string `json:"trace_id,omitempty"`
		// RetryAfterSeconds is populated on rate-limit errors so JSON
		// clients can render a "try again in N seconds" countdown
		// without parsing the Retry-After header. Zero / omitted =
		// no specific hint.
		RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	} `json:"error"`
}

// WriteError maps a domain error to an HTTP response. Unknown errors are
// surfaced as 500 with a generic message; the underlying error is NOT echoed.
func WriteError(w http.ResponseWriter, err error) {
	e := errors.As(err)
	if e == nil {
		e = errors.Internal("internal", "an unexpected error occurred")
	}

	status := statusForKind(e.Kind)
	resp := errorResponse{}
	resp.Error.Code = e.Code
	resp.Error.Kind = string(e.Kind)
	resp.Error.Message = e.Message
	// TraceID could be populated from a request-scoped trace id if added.

	// Surface the retry hint on rate-limit responses both as the
	// standard `Retry-After` header (per RFC 7231 §7.1.3) and in the
	// JSON envelope so SPA clients don't have to parse headers.
	if e.Kind == errors.KindRateLimited && e.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
		resp.Error.RetryAfterSeconds = e.RetryAfterSeconds
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func statusForKind(k errors.Kind) int {
	switch k {
	case errors.KindValidation:
		return http.StatusBadRequest
	case errors.KindUnauthorized:
		return http.StatusUnauthorized
	case errors.KindForbidden:
		return http.StatusForbidden
	case errors.KindNotFound:
		return http.StatusNotFound
	case errors.KindConflict:
		return http.StatusConflict
	case errors.KindPreconditionEx:
		return http.StatusPreconditionFailed
	case errors.KindRateLimited:
		return http.StatusTooManyRequests
	case errors.KindUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// DecodeJSON deserializes the request body into dst.
// Returns a validation error on malformed JSON.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.Validation("body.invalid", "invalid request body")
	}
	return nil
}

// NewRequestID returns a UUIDv4 — used by tests, or when generating IDs
// outside of the chi middleware chain.
func NewRequestID() string { return uuid.NewString() }
