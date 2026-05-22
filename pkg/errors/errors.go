// Package errors defines the domain error taxonomy used across bounded contexts.
//
// Domain code returns these typed errors. The HTTP adapter maps them to status
// codes (see pkg/httpserver). Never leak driver errors (pgx, redis, etc.) past
// the adapter boundary — wrap them in one of these.
package errors

import (
	"errors"
	"fmt"
)

// Kind categorizes a domain error. The HTTP adapter uses this to pick a
// status code; the message body comes from Error.Message.
type Kind string

const (
	KindValidation     Kind = "validation"      // 400 — caller sent invalid data
	KindNotFound       Kind = "not_found"       // 404 — resource missing
	KindConflict       Kind = "conflict"        // 409 — duplicate, version skew, etc.
	KindUnauthorized   Kind = "unauthorized"    // 401 — missing or invalid auth
	KindForbidden      Kind = "forbidden"       // 403 — authed but lacks permission
	KindUnavailable    Kind = "unavailable"     // 503 — dependency failure
	KindInternal       Kind = "internal"        // 500 — unexpected; bug or infra
	KindPreconditionEx Kind = "precondition"    // 412 — preconditions not satisfied
	KindRateLimited    Kind = "rate_limited"    // 429 — caller exceeded rate budget
)

// Error is the canonical domain error type.
type Error struct {
	Kind    Kind
	Code    string // stable machine-readable code, e.g. "user.email_taken"
	Message string // human-readable, safe to surface to the API caller
	cause   error  // optional wrapped error for logging; never exposed

	// RetryAfterSeconds is set on KindRateLimited errors to tell the
	// caller when it's safe to try again. The HTTP adapter renders this
	// as the standard `Retry-After: N` response header AND as a
	// `retry_after_seconds` field on the error envelope so JSON clients
	// don't have to parse headers. Zero = no specific value (caller
	// should back off heuristically).
	RetryAfterSeconds int
}

// WithRetryAfter attaches a retry hint to a rate-limit error. Callers
// outside KindRateLimited shouldn't use this — the adapter ignores it.
func (e *Error) WithRetryAfter(seconds int) *Error {
	e.RetryAfterSeconds = seconds
	return e
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// New constructs a domain error.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Wrap attaches a low-level cause to a domain error (for logs, not API).
func Wrap(kind Kind, code, message string, cause error) *Error {
	return &Error{Kind: kind, Code: code, Message: message, cause: cause}
}

// As extracts a *Error from any error in the chain. Returns nil if not present.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// Common constructors — convenience helpers for the most frequent cases.
func Validation(code, msg string) *Error   { return New(KindValidation, code, msg) }
func NotFound(code, msg string) *Error     { return New(KindNotFound, code, msg) }
func Conflict(code, msg string) *Error     { return New(KindConflict, code, msg) }
func Unauthorized(code, msg string) *Error { return New(KindUnauthorized, code, msg) }
func Forbidden(code, msg string) *Error    { return New(KindForbidden, code, msg) }
func Internal(code, msg string) *Error     { return New(KindInternal, code, msg) }

// Kind sniffers — callers can ask "is this a NotFound?" without
// inspecting strings. Same shape for each so adding new sniffers is
// mechanical.
func IsNotFound(err error) bool { return KindOf(err) == KindNotFound }
func IsConflict(err error) bool { return KindOf(err) == KindConflict }

// KindOf returns the Kind of a typed Error, or KindInternal as a
// safe fallback if `err` isn't one of ours.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	if e, ok := err.(*Error); ok {
		return e.Kind
	}
	return KindInternal
}
