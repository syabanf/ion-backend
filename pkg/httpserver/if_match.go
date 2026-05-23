// Package httpserver — Wave 104 If-Match optimistic-locking middleware.
//
// The middleware satisfies TC-EDGE-021 (stale optimistic-lock retry) and
// the "Optimistic Lock Stale Version" line in the Wave-91 audit doc's
// Edge Case bucket. The contract:
//
//   - On mutating methods (PUT, PATCH, POST, DELETE), the request MUST
//     carry an `If-Match` header whose canonical form is a weak ETag
//     `W/"<row_version>"`. Bare quoted strings (`"abc"`) are also
//     accepted to ease curl-from-the-CLI testing.
//   - The middleware calls the supplied `rowVersionFunc(ctx, id)` to read
//     the current canonical version of the addressed row. The `id` is
//     pulled from the chi URL param `id`.
//   - Comparison is byte-equal on the unwrapped tag value. A mismatch
//     returns 412 Precondition Failed with code `if_match.stale`.
//   - A missing `If-Match` on a mutating request returns 428 Precondition
//     Required with code `if_match.required`.
//   - On a successful match, the middleware writes the resolved version
//     into the request context (via `IfMatchVersionFromContext`) so the
//     handler can echo the *new* version on the response `ETag` header
//     after persisting its mutation.
//   - Non-mutating methods (GET, HEAD, OPTIONS) pass through untouched —
//     no header lookup, no callback firing.
//
// The middleware intentionally couples to the chi URL param shape
// (`{id}`) — every enterprise mutate endpoint in the codebase follows
// that convention. A future "compound key" endpoint can wrap a custom
// `RequireIfMatchByKey` that extracts a different scheme.
package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// RowVersionFunc is the host-supplied lookup that returns the current
// canonical row-version for a given id. Implementations typically read
// `revision::text` or `updated_at::text` from the addressed table.
//
// Returning a typed `errors.NotFound` is acceptable — the middleware
// surfaces it to the client as 404 via the standard error mapper.
type RowVersionFunc func(ctx context.Context, id uuid.UUID) (string, error)

// IfMatchOptions tunes the middleware's behavior. The zero value is the
// canonical Wave-104 contract; future routes can opt in to e.g. allowing
// `*` to bypass the check by tweaking these fields.
type IfMatchOptions struct {
	// URLParam — chi URL parameter name carrying the row id. Defaults
	// to "id" when zero.
	URLParam string
	// Noun — used to namespace the validation-error codes ("boq.id_invalid",
	// "customer_po.id_invalid", ...). Defaults to "resource".
	Noun string
}

func (o IfMatchOptions) urlParam() string {
	if o.URLParam == "" {
		return "id"
	}
	return o.URLParam
}

func (o IfMatchOptions) noun() string {
	if o.Noun == "" {
		return "resource"
	}
	return o.Noun
}

type ctxKeyIfMatchVersion struct{}

// IfMatchVersionFromContext returns the row-version that the middleware
// verified against the incoming request. Empty string when the
// middleware did not run (non-mutating request or wildcard match).
func IfMatchVersionFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyIfMatchVersion{}).(string)
	return v
}

// EtagFor returns the canonical weak-ETag form of a row-version string:
// `W/"<value>"`. Use this when writing the response `ETag` header so
// every endpoint produces identical syntax for the same logical version.
func EtagFor(s string) string {
	return `W/` + strconv.Quote(s)
}

// parseIfMatchTag normalises the incoming If-Match value:
//   - strips a leading `W/` prefix (weak-ETag marker)
//   - strips surrounding double-quotes
//
// Returns the bare row-version string. A bare unquoted token is also
// accepted for CLI ergonomics.
func parseIfMatchTag(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "W/")
	v = strings.TrimPrefix(v, "w/")
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// isMutating reports whether the HTTP method changes server state.
// PUT / PATCH / POST / DELETE require the If-Match header per the
// audit doc; the rest pass through.
func isMutating(method string) bool {
	switch method {
	case http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete:
		return true
	default:
		return false
	}
}

// RequireIfMatch returns a chi-compatible middleware that enforces the
// If-Match contract documented at the top of this file. Use the
// `RequireIfMatchOpts` variant if you need to customise the URL param
// name or error noun.
func RequireIfMatch(rv RowVersionFunc) func(http.Handler) http.Handler {
	return RequireIfMatchOpts(rv, IfMatchOptions{})
}

// RequireIfMatchOpts is the configurable form of RequireIfMatch.
func RequireIfMatchOpts(rv RowVersionFunc, opts IfMatchOptions) func(http.Handler) http.Handler {
	noun := opts.noun()
	param := opts.urlParam()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMutating(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			header := r.Header.Get("If-Match")
			if strings.TrimSpace(header) == "" {
				// RFC 6585 §3 — 428 Precondition Required is the right
				// status for "you forgot the conditional header". Bypass
				// the generic WriteError (which would map to 412) so the
				// missing-vs-stale distinction is visible on the wire.
				writeIfMatchRequired(w)
				return
			}
			id, err := uuid.Parse(chi.URLParam(r, param))
			if err != nil {
				WriteError(w, errors.Validation(noun+"."+param+"_invalid",
					noun+" "+param+" is not a valid uuid"))
				return
			}
			current, err := rv(r.Context(), id)
			if err != nil {
				WriteError(w, err)
				return
			}
			supplied := parseIfMatchTag(header)
			if supplied != current {
				WriteError(w, errors.New(
					errors.KindPreconditionEx,
					"if_match.stale",
					"If-Match header does not match the current row version",
				))
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyIfMatchVersion{}, current)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Override: WriteError of an if_match.required result writes 412 (mapped
// from KindPreconditionEx). RFC 6585 §3 says 428 Precondition Required
// is the correct code for "this request needs a precondition header".
// We add a small specialised writer here so the body code stays
// `if_match.required` for client routing.
//
// (Implementation detail: WriteError is already invoked above. Because
// the middleware ordering writes status FIRST then body, we cannot
// rewrite status after the fact. Refactor the middleware to write the
// 428 directly without going through WriteError for the missing-header
// case.)
//
// Re-implementing the missing-header branch:
func writeIfMatchRequired(w http.ResponseWriter) {
	// Tiny inline JSON response; identical envelope to errorResponse.
	body := []byte(`{"error":{"code":"if_match.required","kind":"precondition","message":"If-Match header is required for this request"}}`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPreconditionRequired) // 428
	_, _ = w.Write(body)
}
