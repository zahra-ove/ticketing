package logger_test

import (
	"bytes"
	"encoding/json"
	"testing"

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
