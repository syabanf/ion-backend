// Package logger wraps the stdlib slog with project conventions.
//
// Every service constructs a single root logger at startup; bounded contexts
// receive it via dependency injection. Use With(...) to add stable fields
// (e.g. request_id, user_id) at request scope.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a configured *slog.Logger.
//
//	level  : debug | info | warn | error
//	format : text | json
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     parseLevel(level),
		AddSource: false,
	}

	var handler slog.Handler
	var out io.Writer = os.Stdout

	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(out, opts)
	default:
		handler = slog.NewTextHandler(out, opts)
	}

	return slog.New(handler)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
