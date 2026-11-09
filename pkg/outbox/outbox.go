// Package outbox implements the transactional outbox pattern.
//
// A service that changes its data and must announce the change cannot write
// to its database and to Kafka atomically. Instead it inserts the message into
// an "outbox" table in the same database transaction as the change. A relay
// then publishes pending rows to Kafka and marks them as published. Either
// both the change and the message are stored, or neither is; publishing is
// at-least-once, so consumers must be idempotent.
//
// Each service owns an outbox table with this schema (see its migrations):
//
//	CREATE TABLE outbox (
//	    id           uuid PRIMARY KEY,
//	    topic        text NOT NULL,
//	    key          text NOT NULL,
//	    type         text NOT NULL,
//	    payload      bytea NOT NULL,
//	    headers      jsonb NOT NULL,
//	    created_at   timestamptz NOT NULL DEFAULT now(),
//	    published_at timestamptz
//	);
//	CREATE INDEX outbox_pending_idx ON outbox (created_at) WHERE published_at IS NULL;
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/zahra-ove/ticketing/pkg/messaging"
)

// Add stores msg in the outbox inside tx. The current trace context is saved
// with the message, so the trace continues when the relay publishes it and
// the consumer handles it, even minutes later.
func Add(ctx context.Context, tx pgx.Tx, msg messaging.Message) error {
	headers := make(map[string]string, len(msg.Headers)+2)
	for k, v := range msg.Headers {
		headers[k] = v
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))

	h, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("encode outbox headers: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (id, topic, key, type, payload, headers)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		msg.ID, msg.Topic, msg.Key, msg.Type, msg.Payload, h)
	if err != nil {
		return fmt.Errorf("insert outbox message: %w", err)
	}
	return nil
}
