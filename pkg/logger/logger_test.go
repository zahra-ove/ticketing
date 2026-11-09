package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/zahra-ove/ticketing/pkg/logger"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     logger.Config
		wantErr bool
	}{
		{name: "defaults to json", cfg: logger.Config{Level: "info"}},
		{name: "text format", cfg: logger.Config{Level: "debug", Format: logger.FormatText}},
		{name: "level is case insensitive", cfg: logger.Config{Level: "WARN"}},
		{name: "unknown level", cfg: logger.Config{Level: "verbose"}, wantErr: true},
		{name: "unknown format", cfg: logger.Config{Level: "info", Format: "xml"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := logger.New(&bytes.Buffer{}, tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNew_AddsServiceAttributes(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log, err := logger.New(&buf, logger.Config{Level: "info", Service: "booking", Version: "v1.2.3"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if got := line["service"]; got != "booking" {
		t.Errorf("service = %v, want booking", got)
	}
	if got := line["version"]; got != "v1.2.3" {
		t.Errorf("version = %v, want v1.2.3", got)
	}
}

func TestNew_AddsTraceContext(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log, err := logger.New(&buf, logger.Config{Level: "info"})
	if err != nil {
		t.Fatal(err)
	}

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	log.InfoContext(ctx, "inside a span")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["trace_id"] != traceID.String() || line["span_id"] != spanID.String() {
		t.Errorf("trace context missing: %v", line)
	}
}

func TestNew_FansOutToExtraHandlersAtSameLevel(t *testing.T) {
	t.Parallel()

	var stdout, extra bytes.Buffer
	log, err := logger.New(&stdout, logger.Config{Level: "warn"},
		slog.NewJSONHandler(&extra, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err != nil {
		t.Fatal(err)
	}

	log.Info("dropped everywhere")
	log.Warn("kept everywhere")

	for name, buf := range map[string]*bytes.Buffer{"stdout": &stdout, "extra": &extra} {
		if got := strings.Count(buf.String(), "\n"); got != 1 {
			t.Errorf("%s got %d lines, want 1: %q", name, got, buf.String())
		}
	}
	if strings.Contains(extra.String(), `"service"`) {
		t.Errorf("extra handler should not get service attributes: %q", extra.String())
	}
}

func TestNew_FiltersBelowLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log, err := logger.New(&buf, logger.Config{Level: "warn"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	log.Info("should be dropped")

	if buf.Len() != 0 {
		t.Errorf("expected no output below warn level, got %q", buf.String())
	}
}
