# 6. Payment saga with a transactional outbox and Kafka

Date: 2026-11-09

## Status

Accepted

## Context

A booking spans two services with their own databases: booking (seats) and
payment (money). There is no distributed transaction between them, yet the end
state must be consistent: a confirmed booking has a successful payment, and a
customer who paid for a booking that did not happen gets their money back.
Either service, Kafka, or the payment provider can be down for a while.

## Decision

**Choreographed saga.** Services react to each other's events; there is no
central orchestrator.

```
booking                         Kafka                         payment
BookingCreated  ───────────────────────────────────────────▶  charge (idempotency key = booking id)
                ◀──────────────  PaymentSucceeded / PaymentFailed
confirm  /  cancel (payment_failed, seats freed)
cancelled already? ─ RefundRequested ──────────────────────▶  refund
                ◀──────────────  PaymentRefunded
```

- **Timeout.** A sweeper in the booking service cancels bookings that waited
  longer than `PAYMENT_TIMEOUT` for their payment and frees the seats. If the
  payment arrives later, the booking requests a refund. The saga therefore
  always ends in a consistent state, even if the payment service was down.
- **Transactional outbox.** Each service writes its events to an `outbox`
  table in the same transaction as its state change; a relay publishes them
  to Kafka (`FOR UPDATE SKIP LOCKED`, at-least-once). This avoids the
  dual-write problem: there is no state change without its event, and no
  event for a change that was rolled back.
- **Domain events.** Aggregates record events (`BookingCreated`, ...); the
  repository maps them to the public protobuf contract and stores them in the
  outbox. Use cases never touch Kafka.
- **Contracts.** Events are protobuf messages (`api/proto/ticketing/*/v1`),
  encoded as protojson so they remain readable in the Redpanda console.
  Metadata follows the CloudEvents Kafka binding (`ce_id`, `ce_type`, ...).
  Messages are keyed by booking id, so every event of a booking stays ordered.
- **Consumers** commit offsets only after handling a record. Transient errors
  are retried with exponential backoff; a record that keeps failing (or cannot
  be decoded) goes to `<topic>.dlq` with the error in a header, so a poison
  message cannot block its partition.
- **Idempotency everywhere,** because delivery is at-least-once:
  - payment: a unique `booking_id` and a provider idempotency key — a booking
    is never charged twice;
  - booking: state checks under a row lock (a duplicate `PaymentSucceeded` for
    a confirmed booking is a no-op);
  - refund: a refunded payment is left alone, and the provider refund has its
    own idempotency key.
- **Tracing across Kafka.** The trace context is stored with the outbox row
  and travels in the record headers, so a single trace shows the HTTP request,
  the outbox publish, the payment, and the confirmation, even when minutes pass
  between them.

## Consequences

- No step depends on another service being up at the same time; a service
  that comes back simply works through its backlog.
- The system is eventually consistent: right after `POST /v1/bookings` the
  booking is `pending_payment`; clients poll the booking (or, later, get
  notified).
- Choreography keeps services decoupled. If the saga grows more steps, an
  orchestrator (a state machine in the booking service) would make the flow
  easier to follow.
- Outbox rows are deleted a day after publishing. Ordering is guaranteed per
  booking only while a single relay instance per service publishes; running
  several relays would need partitioned claiming.
