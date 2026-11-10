# Load test: flash sale

`make load-test` runs [scripts/loadtest/flash-sale.js](../scripts/loadtest/flash-sale.js)
with k6 against the gateway, exactly like real clients: every virtual user logs
in, gets its own JWT, and then races the others for the 200 seats of a freshly
published event, holding one or two neighbouring seats and booking them. After
the run, [verify.sql](../scripts/loadtest/verify.sql) checks the booking
database.

## What must hold

1. **No seat is ever in two active bookings.**
2. Every booking leaves `pending_payment` (the saga completes).
3. No server errors: `409 seat taken` and `429 rate limited` are expected
   answers in a flash sale; anything else counts as a failure.

## Results

Measured on 2026-11-10 on a Windows laptop, Docker Desktop VM with **4 vCPUs
and 8 GB RAM**, running **everything at once**: 7 services, Postgres, Redis,
Redpanda, Traefik, the OpenTelemetry Collector, Prometheus, Tempo, Loki,
Grafana and k6 itself, with 100% of traces sampled.

| Build | Virtual users | Requests/s | Hold p95 | Booking p95 | Server errors | Seats sold twice | Bookings confirmed |
|---|---|---|---|---|---|---|---|
| Go 1.25 | 50 | 43 | 1.13 s | 0.74 s | 0% | **0** | 159 / 159 |
| Go 1.25 | 200 | 28 | 4.8–6.3 s | 4.3–5.5 s | 0% | **0** | all, after the saga drained |
| Go 1.26, upgraded dependencies | 50 (default) | 104 | 0.29 s | 1.18 s | 0% | **0** | 148 / 148 |

The last run came after moving to Go 1.26 and upgrading OpenTelemetry, gRPC
and pgx (done to clear `govulncheck` findings). Part of the gain may also come
from a quieter machine: on a shared laptop the absolute numbers move between
runs, while the correctness results never did.

In every run 199 of 200 seats were sold (the last seat is usually left over
because most customers ask for two neighbouring seats), and every seat
conflict was answered with a clean `409`.

## Where the time goes

Throughput **drops** from 50 to 200 users instead of levelling off: the
signature of saturation, not of a lock or a serial section in the code. A slow
trace from the 200-user run (Tempo, `{resource.service.name="gateway" && duration > 3s}`)
shows it:

```
gateway  POST /v1/holds                     3516 ms
gateway    AuthRequest                       654 ms   (auth service itself: 17 ms)
booking    BookingService.HoldSeats         1484 ms
booking      CatalogService/GetSeats (gRPC)  542 ms   (event service itself: 297 ms)
booking      pool.acquire                    690 ms
booking      query SELECT                    252 ms
```

The work is fast; the gaps between spans are time spent waiting for a CPU.
At that moment the observability stack (Tempo, Loki, Collector) used about as
much CPU as the services it observes.

What was tried: doubling the Postgres pool size (10 → 20) changed nothing
measurable, which confirmed that connection waits were a symptom of CPU
starvation, not the cause. The change was reverted.

## What would scale it

In order of expected impact on real infrastructure:

1. **Run the observability stack elsewhere and sample traces** (for example
   10% head sampling, or tail sampling in the collector that keeps all errors
   and slow requests).
2. **Scale the booking and event services horizontally.** Both are stateless;
   the seat lock lives in Redis and the final guarantee in Postgres, so
   replicas need no coordination.
3. **Cache catalog lookups.** Seat prices and labels rarely change; caching
   `GetSeats` in the booking service (or in Redis) removes a gRPC call and two
   queries from every hold.
4. **Verify JWTs in the gateway** (or cache ForwardAuth results) to remove one
   internal hop per request.
