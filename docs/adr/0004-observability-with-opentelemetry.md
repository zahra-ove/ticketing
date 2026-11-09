# 4. Observability with OpenTelemetry and the Grafana stack

Date: 2026-11-09

## Status

Accepted

## Context

A request in this system crosses several services, Postgres and Kafka. When it
is slow or fails, we need to answer "where and why" quickly. That needs traces,
metrics and logs that point to each other.

## Decision

- **Instrumentation:** the OpenTelemetry Go SDK for all three signals, set up once
  in `pkg/telemetry`. Exporters are configured only through the standard `OTEL_*`
  environment variables, so the code does not know which backend is used.
- **Transport:** every service sends OTLP to one OpenTelemetry Collector. The
  collector batches, protects itself with a memory limiter, and routes:
  traces to Tempo, metrics to Prometheus (native OTLP receiver), logs to Loki
  (native OTLP endpoint).
- **Logs:** `log/slog` writes JSON to stdout (for `docker logs`) and, through the
  `otelslog` bridge, to OTLP. Every log line written inside a span carries its
  `trace_id` and `span_id`.
- **What is instrumented:**
  - HTTP server: `otelhttp`, with spans and metrics labelled by route template
    (`GET /v1/events/{id}`), never by raw path, to keep metric cardinality bounded.
  - Use cases: a decorator around the application service adds one span per use
    case and business counters, keeping telemetry out of the business code.
    Expected errors (validation, not found, conflict) do not mark spans as failed.
  - Postgres: `otelpgx` adds one span per query and connection pool metrics.
  - Go runtime: memory, GC and goroutine metrics.
- **Grafana** is provisioned from files (datasources and dashboards), with links
  between signals: exemplar to trace, trace to logs, log line to trace. Tempo's
  metrics generator adds a service graph and span metrics.

## Consequences

- Swapping a backend (for example Jaeger, or a hosted vendor) is a collector
  config change, with no code change.
- Local runs without the collector set `OTEL_SDK_DISABLED=true`.
- Everything is sampled locally (`parentbased_always_on`); production would use a
  ratio sampler or tail sampling in the collector.
