-- +goose Up
-- Inbox: ids of messages already handled, so redelivered messages are skipped.
CREATE TABLE inbox (
    message_id   uuid        PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inbox_processed_at_idx ON inbox (processed_at);

-- +goose Down
DROP TABLE inbox;
