-- +goose Up
CREATE TABLE payments (
    id                 uuid        PRIMARY KEY,
    -- One payment per booking: the database enforces "never charge twice".
    booking_id         uuid        NOT NULL UNIQUE,
    customer_id        uuid        NOT NULL,
    amount_cents       bigint      NOT NULL CHECK (amount_cents >= 0),
    status             text        NOT NULL CHECK (status IN ('succeeded', 'failed', 'refunded')),
    failure_reason     text,
    provider_reference text,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL
);

CREATE TABLE outbox (
    id           uuid        PRIMARY KEY,
    topic        text        NOT NULL,
    key          text        NOT NULL,
    type         text        NOT NULL,
    payload      bytea       NOT NULL,
    headers      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX outbox_pending_idx ON outbox (created_at) WHERE published_at IS NULL;

-- +goose Down
DROP TABLE outbox;
DROP TABLE payments;
