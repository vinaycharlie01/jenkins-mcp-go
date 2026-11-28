// Package logging builds the slog logger with secret redaction.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// sensitive attribute-key fragments whose values are always redacted.
var sensitive = []string{"token", "cookie", "session", "password", "secret", "authorization", "credential"}

const redacted = "[REDACTED]"

// New returns a logger writing to w (stderr: stdout carries the MCP stream).
func New(w io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level), ReplaceAttr: redact}
	if strings.EqualFold(format, "text") {
		return slog.New(slog.NewTextHandler(w, opts))
	}

	return slog.New(slog.NewJSONHandler(w, opts))
}

// redact replaces the value of any attribute whose key looks sensitive.
func redact(_ []string, a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	for _, s := range sensitive {
		if strings.Contains(k, s) {
			return slog.String(a.Key, redacted)
		}
	}

	return a
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
