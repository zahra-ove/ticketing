-- +goose Up
ALTER TABLE bookings
    ADD COLUMN cancel_reason text
        CHECK (cancel_reason IN ('customer', 'payment_failed', 'payment_timeout'));

-- The payment-timeout sweeper looks for old pending bookings.
CREATE INDEX bookings_pending_created_idx ON bookings (created_at) WHERE status = 'pending_payment';

-- Transactional outbox: events are written here in the same transaction as
-- the booking change and published to Kafka by the relay.
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
DROP INDEX bookings_pending_created_idx;
ALTER TABLE bookings DROP COLUMN cancel_reason;
