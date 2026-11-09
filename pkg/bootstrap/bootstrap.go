// Package bootstrap wires the process-wide pieces every service starts with:
// OpenTelemetry and a structured logger connected to it.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"

	"github.com/zahra-ove/ticketing/pkg/logger"
	"github.com/zahra-ove/ticketing/pkg/telemetry"
)

// Options describes the service being started.
type Options struct {
	Service           string
	Version           string
	LogLevel          string
	LogFormat         string
	TelemetryDisabled bool
}

// Observability sets up telemetry and returns a logger that writes JSON to
// stdout and exports to OTLP. The returned function flushes buffered
// telemetry; defer it right away so it runs last.
func Observability(ctx context.Context, opts Options) (*slog.Logger, func(), error) {
	tel, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName:    opts.Service,
		ServiceVersion: opts.Version,
		Disabled:       opts.TelemetryDisabled,
	})
	if err != nil {
		return nil, nil, err
	}

	flush := func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			fmt.Fprintf(os.Stderr, "%s: flush telemetry: %v\n", opts.Service, err)
		}
	}

	log, err := logger.New(os.Stdout, logger.Config{
		Level:   opts.LogLevel,
		Format:  logger.Format(opts.LogFormat),
		Service: opts.Service,
		Version: opts.Version,
	}, otelslog.NewHandler(opts.Service, otelslog.WithLoggerProvider(tel.LoggerProvider)))
	if err != nil {
		flush()
		return nil, nil, err
	}

	// Report exporter failures (e.g. collector unreachable) through our logger.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.WarnContext(ctx, "opentelemetry error", slog.Any("error", err))
	}))

	return log, flush, nil
}
