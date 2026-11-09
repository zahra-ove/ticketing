# 5. Seat holds in Redis, bookings in Postgres

Date: 2026-11-09

## Status

Accepted

## Context

Popular events get many customers clicking the same seats at the same moment.
A customer needs a few minutes between choosing seats and paying, and during
that time nobody else may take those seats. Abandoned selections must free
themselves without a cleanup job. Above all, a seat must never be sold twice.

## Decision

Booking is split into two steps with two stores.

**1. Hold (Redis, short-lived).** One key per seat,
`ticketing:{evt:<event>}:seat:<seat>`, whose value is the hold id and whose
TTL is the hold duration (`HOLD_TTL`, 10 minutes by default). A Lua script
checks every requested seat and sets all of them only if all are free, so a
hold is all-or-nothing and two customers can never hold the same seat. Expiry
is Redis' own TTL: abandoned holds disappear on their own. Release and verify
scripts compare the stored hold id, so a late release never frees a seat that
has meanwhile been held by someone else. All keys of an event share the
`{evt:<event>}` hash tag, which keeps the scripts valid on Redis Cluster.

**2. Booking (Postgres, durable).** Turning a hold into a booking writes the
booking and its seats in one transaction. A partial unique index,
`(event_id, seat_id) WHERE active`, makes it impossible for a seat to be in two
active bookings, whatever Redis or the application did. Cancelling a booking
sets `active = false`, which frees the seat in the same index.

Prices are read from the event service over gRPC when seats are held and are
locked into the hold.

**Idempotency.** `bookings.hold_id` is unique. Creating a booking with a hold
that was already used returns the existing booking (HTTP 200 instead of 201),
so clients can safely retry after a timeout.

## Consequences

- Hold contention is handled in memory, in a single round trip, without
  database row locks.
- Postgres stays the source of truth; Redis can be flushed without selling a
  seat twice (customers would only lose their current holds).
- There is a small window where a hold expires just before the booking is
  written and another customer holds the seat. The booking step re-verifies
  ownership of every seat, and the unique index decides any remaining race:
  the first booking wins, the other customer gets 409 with the list of taken seats.
- Holds are invisible to Postgres. The availability endpoint merges booked
  seats (Postgres) with held seats (a per-event sorted set in Redis).
