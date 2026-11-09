-- +goose Up
CREATE TABLE events (
    id          uuid        PRIMARY KEY,
    title       text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    venue       text        NOT NULL,
    starts_at   timestamptz NOT NULL,
    status      text        NOT NULL CHECK (status IN ('draft', 'published', 'cancelled')),
    sections    jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);

-- Serves the public listing: published events ordered by (starts_at, id).
CREATE INDEX events_published_starts_at_idx ON events (starts_at, id) WHERE status = 'published';

CREATE TABLE seats (
    id          uuid    PRIMARY KEY,
    event_id    uuid    NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    section     text    NOT NULL,
    -- Position of the section in the event layout; keeps the seat map in venue order.
    section_index integer NOT NULL CHECK (section_index >= 0),
    row_number  integer NOT NULL CHECK (row_number > 0),
    seat_number integer NOT NULL CHECK (seat_number > 0),
    price_cents bigint  NOT NULL CHECK (price_cents >= 0),
    UNIQUE (event_id, section, row_number, seat_number)
);

-- +goose Down
DROP TABLE seats;
DROP TABLE events;
