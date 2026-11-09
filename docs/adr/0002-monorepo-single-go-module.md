# 2. Monorepo with a single Go module

Date: 2026-11-09

## Status

Accepted

## Context

The system has four services plus a few shared packages and protobuf
contracts. Options were one repository per service, a monorepo with one Go
module per service (tied together with `go.work`), or a monorepo with one module.

## Decision

Use one repository and one Go module (`github.com/zahra-ove/ticketing`).

- Each service lives in `services/<name>/` and keeps its code under
  `services/<name>/internal/`, so the Go compiler stops services from importing
  each other's internals.
- Services communicate only through contracts in `api/proto/` and Kafka events,
  never through shared domain types.
- `pkg/` holds only small, infrastructure-level helpers (logging, telemetry,
  shutdown). Business logic never goes there.

## Consequences

- One `go.mod`, one lint config, one CI pipeline; refactors across services are atomic.
- All services share dependency versions. This is acceptable for a single team;
  if services were owned by separate teams, a module per service would be revisited.
- Each service still builds its own Docker image, so they deploy independently.
