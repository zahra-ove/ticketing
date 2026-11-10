# ticketing

An event ticketing platform built as a set of Go microservices. It is a
portfolio project that focuses on the hard parts of distributed systems:
concurrent seat reservations, distributed transactions (saga), reliable
messaging (transactional outbox), idempotency and end-to-end observability.

> Status: work in progress. Phases 1–7 are done: services, payment saga over Kafka, notifications, observability, and a gateway with JWT authentication.

## Architecture

| Service | Responsibility | Storage | Talks via |
|---|---|---|---|
| `event` | Events, venues and seat catalog | Postgres | HTTP (public), gRPC (internal) |
| `booking` | Seat holds with TTL, bookings, saga orchestration | Postgres, Redis | HTTP, gRPC client, Kafka |
| `payment` | Charges and refunds through a mock provider with idempotency keys | Postgres | Kafka, HTTP (read-only) |
| `notification` | Emails customers about confirmations, cancellations and refunds | Postgres (inbox) | Kafka, SMTP |

Infrastructure: Postgres (database per service), Redis, Redpanda (Kafka API),
Traefik as the API gateway, and an OpenTelemetry pipeline to Prometheus, Tempo,
Loki and Grafana.

```
client ──HTTP──▶ booking ──gRPC──▶ event ──▶ Postgres (event)
                    │
                    ├──▶ Redis     seat holds (atomic Lua, TTL)
                    ├──▶ Postgres  bookings + outbox
                    │
                    └── Kafka (Redpanda) ◀──▶ payment ──▶ Postgres (payments + outbox)
                        booking events / payment events  (choreographed saga)
```

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
| **API gateway** (all API calls) | http://localhost:8000 |
| Traefik dashboard | http://localhost:8090 |
| Grafana (dashboards, traces, logs) | http://localhost:3000 |
| Prometheus | http://localhost:9090 |
| Redpanda Console (topics, messages) | http://localhost:8085 |
| Mailpit (emails sent to customers) | http://localhost:8025 |
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
make obs-up           # infrastructure + observability
make run-event        # migrates, then serves HTTP on :8081 and gRPC on :9000
make run-booking      # migrates, then serves HTTP on :8082
```

### Tests

```bash
make test              # unit tests
make test-integration  # + tests against real Postgres and Redis (testcontainers)
```

## Observability

```
service ──OTLP──▶ OpenTelemetry Collector ──▶ Tempo       (traces)
                                         ├──▶ Prometheus  (metrics)
                                         └──▶ Loki        (logs)
                                                   ▲
                                       Grafana ────┘ (all three, linked by trace id)
```

- **Traces:** one trace per request across services: HTTP → use case → gRPC → the
  other service → SQL queries and Redis commands.
- **Metrics:** HTTP RED metrics by route, business counters (`ticketing_events_*`, `ticketing_holds_*`, `ticketing_bookings_*`),
  Postgres pool, Redis and Go runtime metrics. Latency histograms carry exemplars that
  link to the trace of a slow request.
- **Logs:** JSON on stdout and OTLP to Loki; every line inside a request carries
  `trace_id`, so Grafana jumps from a log line to its trace and back.
- The **Service overview** dashboard is provisioned in Grafana under *Ticketing*;
  pick the service at the top.

See [ADR 4](docs/adr/0004-observability-with-opentelemetry.md) for the reasoning.

## Authentication

All API calls go through the gateway on `:8000`; services have no public
ports. Get a token, then send it as a bearer token:

```bash
TOKEN=$(curl -s localhost:8000/v1/token -d '{"username":"admin","password":"admin-secret"}' | jq -r .access_token)
curl -H "Authorization: Bearer $TOKEN" localhost:8000/v1/events -d '{...}'
```

| User | Password | Role |
|---|---|---|
| `admin` | `admin-secret` | admin: manages the catalog, reads all payments |
| `alice`, `bob` | `demo-secret` | customer |
| `customer-<anything>` | `demo-secret` | customer (only with `DEMO_SIGNUP=true`, for load tests) |

Reading the catalog needs no token. The gateway strips identity headers sent
by clients and sets them only from a verified token; see
[ADR 7](docs/adr/0007-gateway-and-authentication.md).

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
curl -i localhost:8000/v1/events -H "Authorization: Bearer $ADMIN_TOKEN" -d '{
  "title": "GopherCon Tehran",
  "venue": "Milad Tower",
  "starts_at": "2027-03-01T17:00:00Z",
  "sections": [
    {"name": "VIP", "rows": 2, "seats_per_row": 10, "price_cents": 5000},
    {"name": "Standard", "rows": 20, "seats_per_row": 30, "price_cents": 2000}
  ]
}'
```

## Booking service API

Every route needs a customer token, except availability.

| Method | Path | Description |
|---|---|---|
| `GET` | `/v1/events/{id}/availability` | Booked and currently held seats of an event |
| `POST` | `/v1/holds` | Hold seats for `HOLD_TTL` (all or nothing); 409 lists taken seats |
| `GET` | `/v1/holds/{id}` | Get a hold |
| `DELETE` | `/v1/holds/{id}` | Release a hold early |
| `POST` | `/v1/bookings` | Turn a hold into a booking; idempotent per hold (201 new, 200 retry) |
| `GET` | `/v1/bookings/{id}` | Get a booking |
| `POST` | `/v1/bookings/{id}/cancel` | Cancel an unpaid booking and free its seats |

```bash
curl -i localhost:8000/v1/holds -H "Authorization: Bearer $TOKEN"   -d '{"event_id": "<event id>", "seat_ids": ["<seat id>", "<seat id>"]}'
curl -i localhost:8000/v1/bookings -H "Authorization: Bearer $TOKEN"   -d '{"hold_id": "<hold id>"}'
```

How double booking is prevented is explained in
[ADR 5](docs/adr/0005-seat-holds-in-redis-bookings-in-postgres.md).

## Payment saga

A new booking is `pending_payment`. The booking service publishes
`BookingCreated` (through its outbox); the payment service charges the
customer and publishes `PaymentSucceeded` or `PaymentFailed`; the booking is
then `confirmed` or `cancelled` (seats freed). Bookings unpaid after
`PAYMENT_TIMEOUT` are cancelled, and a payment that arrives for a cancelled
booking is refunded.

```bash
curl -H "Authorization: Bearer $TOKEN" 'localhost:8000/v1/payments?booking_id=<booking id>'
```

To try the failure paths: prices ending in `.13` (e.g. `2513` cents) are
declined by the mock provider; `PROVIDER_FAILURE_RATE` injects transient
errors (retried); stop the payment container to see the timeout and the
refund compensation. Details in [ADR 6](docs/adr/0006-payment-saga-with-outbox-and-kafka.md).

## Notifications

The notification service consumes booking and payment events and emails the
customer when a booking is confirmed, cancelled (with the reason) or refunded.
Every email lands in Mailpit (http://localhost:8025).

Kafka delivers at least once, so the service uses the **inbox pattern**: the
message id is inserted into an `inbox` table in the same transaction that
wraps the send, and a message already in the inbox is skipped. Replaying a
whole topic sends no duplicate emails.

## Repository layout

```
services/<name>/        one directory per service (cmd/, internal/, migrations/, Dockerfile)
pkg/                    shared infrastructure: httpx, grpcx, telemetry, logger, postgres, redisx
api/proto/              protobuf contracts (buf); generated code in api/gen
deploy/compose/         docker compose files for infrastructure, observability and services
docs/adr/               architecture decision records
```

## Roadmap

- [x] Phase 1 — repository skeleton, tooling, CI, infrastructure
- [x] Phase 2 — `event` service end to end
- [x] Phase 3 — observability stack (OpenTelemetry, Prometheus, Tempo, Loki, Grafana)
- [x] Phase 4 — `booking` service, gRPC, seat holds in Redis
- [x] Phase 5 — Kafka, transactional outbox, `payment` service, saga
- [x] Phase 6 — `notification` service
- [x] Phase 7 — Traefik gateway and JWT authentication
- [ ] Phase 8 — dashboards, load tests, hardening
