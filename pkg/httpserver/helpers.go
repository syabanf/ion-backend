package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Helpers — small request-parsing + response-formatting utilities that
// every adapter/http handler reaches for. Living here keeps the per-
// context handlers focused on business logic and removes the silent
// drift we audited in Wave 72 (8 copies of parseIntDefault, 100+ raw
// uuid.Parse(chi.URLParam(...)) blocks, ~200 ad-hoc RFC3339 formats).

// ParseIntDefault parses a string to int, returning def for empty,
// invalid, or non-positive input. Replaces the per-handler copy in
// crm/billing/field/warehouse/network/platform/identity/enterprise.
func ParseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ParseUUIDParam pulls a chi URL parameter, parses it as a uuid, and
// writes a clean Validation error (400) on failure. Returns the parsed
// id and a sentinel ok=false telling the caller to bail. Typical use:
//
//	id, ok := httpserver.ParseUUIDParam(w, r, "id", "wo")
//	if !ok { return }
//
// The `noun` is used to namespace the error code ("wo.id_invalid",
// "branch.id_invalid", ...) so the front-end can localize per surface.
func ParseUUIDParam(w http.ResponseWriter, r *http.Request, paramName, noun string) (uuid.UUID, bool) {
	raw := chi.URLParam(r, paramName)
	id, err := uuid.Parse(raw)
	if err != nil {
		WriteError(w, errors.Validation(noun+"."+paramName+"_invalid",
			noun+" "+paramName+" is not a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

// FormatRFC3339 returns t formatted as UTC RFC3339 — the canonical
// timestamp string the DTOs ship over the wire. Returns "" for nil
// pointers so callers can omitempty them cleanly.
func FormatRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// FormatRFC3339Ptr is the nil-safe counterpart for *time.Time DTO fields.
func FormatRFC3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
