// Package telemetry sets up OpenTelemetry tracing, metrics and logs for a service.
//
// Exporters are configured with the standard OTEL_* environment variables
// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_RESOURCE_ATTRIBUTES, OTEL_TRACES_SAMPLER,
// OTEL_METRIC_EXPORT_INTERVAL, ...), so the same binary works with any
// OTLP-compatible backend without code changes.
//
// Setup registers the providers globally. Instrumentation libraries
// (otelhttp, otelpgx) and the application code pick them up from there, which
// is the convention of the OpenTelemetry Go project.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Config describes the running service.
type Config struct {
	ServiceName    string
	ServiceVersion string
	// Disabled turns every signal into a no-op, e.g. for local runs without a collector.
	Disabled bool
}

// Telemetry holds the configured providers.
type Telemetry struct {
	// LoggerProvider feeds the slog bridge, so log records are exported over OTLP.
	LoggerProvider log.LoggerProvider

	shutdowns []func(context.Context) error
}

// Setup creates and globally registers the trace, metric and log providers.
// Call Shutdown before the process exits to flush buffered telemetry.
func Setup(ctx context.Context, cfg Config) (_ *Telemetry, err error) {
	// W3C trace context lets traces cross HTTP, gRPC and Kafka boundaries.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	t := &Telemetry{LoggerProvider: lognoop.NewLoggerProvider()}
	if cfg.Disabled {
		return t, nil
	}

	// If a later step fails, release whatever was already started.
	defer func() {
		if err != nil {
			err = errors.Join(err, t.Shutdown(context.WithoutCancel(ctx)))
		}
	}()

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp),
	)
	t.shutdowns = append(t.shutdowns, tp.Shutdown)
	otel.SetTracerProvider(tp)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)
	t.shutdowns = append(t.shutdowns, mp.Shutdown)
	otel.SetMeterProvider(mp)

	logExp, err := otlploggrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	t.shutdowns = append(t.shutdowns, lp.Shutdown)
	otel.SetLoggerProvider(lp)
	t.LoggerProvider = lp

	// Go runtime metrics: memory, GC, goroutines, scheduler latency.
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return nil, fmt.Errorf("start runtime metrics: %w", err)
	}

	return t, nil
}

// Shutdown flushes and stops all providers. It is safe to call on a disabled Telemetry.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	// Reverse order: logs and metrics produced while shutting down still get a trace provider.
	for i := len(t.shutdowns) - 1; i >= 0; i-- {
		errs = append(errs, t.shutdowns[i](ctx))
	}
	t.shutdowns = nil
	return errors.Join(errs...)
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	instanceID, err := os.Hostname() // the container id inside Docker
	if err != nil {
		instanceID = "unknown"
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES, e.g. deployment.environment.name
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithProcessRuntimeVersion(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.ServiceInstanceID(instanceID),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}
	return res, nil
}
