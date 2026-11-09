// Package logger builds the structured [slog.Logger] shared by every service.
//
// All services log as JSON to stdout so that the log collector can ship the
// lines to Loki without any parsing rules. Every line carries the service name
// and version, which makes it possible to filter logs per service and release.
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Format is the output encoding of the logger.
type Format string

// Supported log formats.
const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// Config describes how a logger is built.
type Config struct {
	// Level is one of debug, info, warn or error (case-insensitive).
	Level string
	// Format is json (default) or text. Text is handy for local development.
	Format Format
	// Service is the name of the running service, e.g. "booking".
	Service string
	// Version is the build version of the running service.
	Version string
}

// New returns a logger that writes to w according to cfg.
//
// Lines written to w get the trace_id and span_id of the active span, so a
// log line can be matched to its trace. Every record is also passed to the
// extra handlers (for example the OpenTelemetry slog bridge) at the same level.
func New(w io.Writer, cfg Config, extra ...slog.Handler) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", cfg.Level, err)
	}

	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	switch Format(strings.ToLower(string(cfg.Format))) {
	case FormatJSON, "":
		h = slog.NewJSONHandler(w, opts)
	case FormatText:
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}

	// service and version go only to stdout: OTLP records carry them as resource attributes.
	stdout := traceContextHandler{h}.WithAttrs([]slog.Attr{
		slog.String("service", cfg.Service),
		slog.String("version", cfg.Version),
	})
	if len(extra) == 0 {
		return slog.New(stdout), nil
	}

	handlers := []slog.Handler{stdout}
	for _, e := range extra {
		handlers = append(handlers, levelHandler{Handler: e, level: level})
	}
	return slog.New(fanoutHandler(handlers)), nil
}
