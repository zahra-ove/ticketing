-- +goose Up
CREATE TABLE bookings (
    id          uuid        PRIMARY KEY,
    -- One booking per hold: makes "create booking" idempotent for retries.
    hold_id     uuid        NOT NULL UNIQUE,
    event_id    uuid        NOT NULL,
    customer_id uuid        NOT NULL,
    status      text        NOT NULL CHECK (status IN ('pending_payment', 'confirmed', 'cancelled')),
    total_cents bigint      NOT NULL CHECK (total_cents >= 0),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);

CREATE INDEX bookings_customer_created_idx ON bookings (customer_id, created_at DESC);

CREATE TABLE booking_seats (
    booking_id  uuid    NOT NULL REFERENCES bookings (id) ON DELETE CASCADE,
    event_id    uuid    NOT NULL,
    seat_id     uuid    NOT NULL,
    label       text    NOT NULL,
    price_cents bigint  NOT NULL CHECK (price_cents >= 0),
    -- Denormalised from bookings.status so the unique index below can use it.
    active      boolean NOT NULL DEFAULT true,
    PRIMARY KEY (booking_id, seat_id)
);

-- The final guarantee against double booking: a seat can be in at most one
-- active booking, whatever happens in Redis or in application code.
CREATE UNIQUE INDEX booking_seats_one_active_booking_per_seat
    ON booking_seats (event_id, seat_id)
    WHERE active;

-- +goose Down
DROP TABLE booking_seats;
DROP TABLE bookings;
