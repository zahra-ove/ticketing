# ticketing

An event ticketing platform built as a set of Go microservices. It is a
portfolio project that focuses on the hard parts of distributed systems:
concurrent seat reservations, distributed transactions (saga), reliable
messaging (transactional outbox), idempotency and end-to-end observability.

> Status: work in progress. Phases 1–2 are done: repository skeleton, infrastructure and the `event` service.

## Architecture

| Service | Responsibility | Storage | Talks via |
|---|---|---|---|
| `event` | Events, venues and seat catalog | Postgres, Redis cache | HTTP (public), gRPC (internal) |
| `booking` | Seat holds with TTL, bookings, saga orchestration | Postgres, Redis | gRPC, Kafka |
| `payment` | Mock payment provider with idempotency keys | Postgres | Kafka |
| `notification` | Sends booking emails | — | Kafka |

Infrastructure: Postgres (database per service), Redis, Redpanda (Kafka API),
Traefik as the API gateway, and an OpenTelemetry pipeline to Prometheus, Tempo,
Loki and Grafana.

Architecture decisions are recorded in [docs/adr](docs/adr).

## Getting started

Prerequisites: Go 1.25+, Docker with Compose v2, Make, and
[golangci-lint v2](https://golangci-lint.run/) (`make tools`).

```bash
make infra-up   # Postgres, Redis, Redpanda and Redpanda Console
make check      # lint, tests with -race, govulncheck
```

| Tool | URL |
|---|---|
| Redpanda Console | http://localhost:8085 |
| Postgres | `localhost:5432` |
| Redis | `localhost:6379` |
| Kafka (from host) | `localhost:19092` |

Run `make` to see every target.

### Run the whole stack in Docker

```bash
make services-up      # builds images, runs migrations, starts everything, waits until healthy
```

### Run a service locally (hot path while coding)

```bash
make infra-up
make run-event        # migrates, then serves on http://localhost:8081
```

### Tests

```bash
make test              # unit tests
make test-integration  # + repository tests against a real Postgres (testcontainers)
```

## Event service API

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/events` | Create a draft event and generate its seats |
| `GET` | `/v1/events?limit=&cursor=` | List published events (keyset pagination) |
| `GET` | `/v1/events/{id}` | Get one event |
| `POST` | `/v1/events/{id}/publish` | Publish a draft event |
| `POST` | `/v1/events/{id}/cancel` | Cancel an event |
| `GET` | `/v1/events/{id}/seats` | Seat map of an event |
| `GET` | `/livez`, `/readyz` | Liveness and readiness probes |

Errors use [RFC 9457 problem details](https://www.rfc-editor.org/rfc/rfc9457)
(`application/problem+json`) and carry the `X-Request-ID` of the request.

```bash
curl -i localhost:8081/v1/events -d '{
  "title": "GopherCon Tehran",
  "venue": "Milad Tower",
  "starts_at": "2027-03-01T17:00:00Z",
  "sections": [
    {"name": "VIP", "rows": 2, "seats_per_row": 10, "price_cents": 5000},
    {"name": "Standard", "rows": 20, "seats_per_row": 30, "price_cents": 2000}
  ]
}'
```

## Repository layout

```
services/<name>/        one directory per service (cmd/, internal/, migrations/, Dockerfile)
pkg/                    small, genuinely shared packages (logging, telemetry, shutdown)
api/proto/              protobuf contracts
deploy/compose/         docker compose files for infrastructure, observability and services
docs/adr/               architecture decision records
```

## Roadmap

- [x] Phase 1 — repository skeleton, tooling, CI, infrastructure
- [x] Phase 2 — `event` service end to end
- [ ] Phase 3 — observability stack (OpenTelemetry, Prometheus, Tempo, Loki, Grafana)
- [ ] Phase 4 — `booking` service, gRPC, seat holds in Redis
- [ ] Phase 5 — Kafka, transactional outbox, `payment` service, saga
- [ ] Phase 6 — `notification` service
- [ ] Phase 7 — Traefik gateway and JWT authentication
- [ ] Phase 8 — dashboards, load tests, hardening
