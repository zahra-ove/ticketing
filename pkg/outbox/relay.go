package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
)

// Producer is the part of *kgo.Client the relay uses.
type Producer interface {
	ProduceSync(ctx context.Context, rs ...*kgo.Record) kgo.ProduceResults
}

// RelayConfig configures a Relay.
type RelayConfig struct {
	// Interval is the pause between polls when the outbox is empty. Default 500ms.
	Interval time.Duration
	// BatchSize is the maximum number of messages published per transaction. Default 100.
	BatchSize int
	// Retention is how long published rows are kept before cleanup. Default 24h.
	Retention time.Duration
}

// Relay publishes pending outbox rows to Kafka.
type Relay struct {
	pool     *pgxpool.Pool
	producer Producer
	log      *slog.Logger
	cfg      RelayConfig

	published metric.Int64Counter
	lastClean time.Time
}

// NewRelay returns a Relay. Several replicas can run at once: rows are
// claimed with FOR UPDATE SKIP LOCKED, so each row is published by one of them.
func NewRelay(pool *pgxpool.Pool, producer Producer, cfg RelayConfig, log *slog.Logger) (*Relay, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 500 * time.Millisecond
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	r := &Relay{pool: pool, producer: producer, log: log, cfg: cfg}

	meter := otel.Meter("github.com/zahra-ove/ticketing/pkg/outbox")
	var err error
	if r.published, err = meter.Int64Counter("ticketing.outbox.published",
		metric.WithDescription("Outbox messages published to Kafka."), metric.WithUnit("{message}")); err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}
	// How far behind the relay is: alert on this, not on individual errors.
	if _, err = meter.Int64ObservableGauge("ticketing.outbox.pending",
		metric.WithDescription("Outbox messages not yet published."), metric.WithUnit("{message}"),
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			var n int64
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
				return err
			}
			o.Observe(n)
			return nil
		})); err != nil {
		return nil, fmt.Errorf("create gauge: %w", err)
	}
	return r, nil
}

// Run publishes until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "outbox relay started")
	for {
		n, err := r.publishBatch(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.ErrorContext(ctx, "outbox publish failed", slog.Any("error", err))
		}
		r.maybeClean(ctx)

		// A full batch means more rows are waiting: go again without sleeping.
		if err == nil && n == r.cfg.BatchSize {
			continue
		}
		select {
		case <-ctx.Done():
			r.log.InfoContext(ctx, "outbox relay stopped")
			return nil
		case <-time.After(r.cfg.Interval):
		}
	}
}

type row struct {
	id      uuid.UUID
	topic   string
	key     string
	payload []byte
	headers map[string]string
}

// publishBatch claims up to BatchSize rows, publishes them, and marks them
// published, all in one transaction. If producing fails the transaction rolls
// back and the rows are retried on the next poll.
func (r *Relay) publishBatch(ctx context.Context) (int, error) {
	var count int
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, topic, key, payload, headers
			FROM outbox
			WHERE published_at IS NULL
			ORDER BY created_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, r.cfg.BatchSize)
		if err != nil {
			return fmt.Errorf("claim outbox rows: %w", err)
		}
		batch, err := pgx.CollectRows(rows, func(cr pgx.CollectableRow) (row, error) {
			var (
				rw  row
				raw []byte
			)
			if err := cr.Scan(&rw.id, &rw.topic, &rw.key, &rw.payload, &raw); err != nil {
				return rw, err
			}
			return rw, json.Unmarshal(raw, &rw.headers)
		})
		if err != nil {
			return fmt.Errorf("read outbox rows: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}

		records := make([]*kgo.Record, len(batch))
		for i, rw := range batch {
			records[i] = r.record(ctx, rw)
		}
		// One ProduceSync for the batch; records with the same key keep their order.
		if err := r.producer.ProduceSync(ctx, records...).FirstErr(); err != nil {
			return fmt.Errorf("produce outbox batch: %w", err)
		}

		ids := make([]uuid.UUID, len(batch))
		for i, rw := range batch {
			ids[i] = rw.id
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, ids); err != nil {
			return fmt.Errorf("mark outbox rows published: %w", err)
		}
		count = len(batch)
		return nil
	})
	if err == nil && count > 0 {
		r.published.Add(ctx, int64(count))
	}
	return count, err
}

// record builds the Kafka record. Its context is the one saved when the
// message was added, so the producer span joins the original trace.
func (r *Relay) record(ctx context.Context, rw row) *kgo.Record {
	traceCtx := otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(rw.headers))

	rec := &kgo.Record{Topic: rw.topic, Key: []byte(rw.key), Value: rw.payload, Context: traceCtx}
	for k, v := range rw.headers {
		// Trace headers are re-injected by the producer hook for its own span.
		if k == "traceparent" || k == "tracestate" {
			continue
		}
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return rec
}

// maybeClean deletes old published rows about once a minute.
func (r *Relay) maybeClean(ctx context.Context) {
	if time.Since(r.lastClean) < time.Minute {
		return
	}
	r.lastClean = time.Now()
	tag, err := r.pool.Exec(ctx, `DELETE FROM outbox WHERE published_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(r.cfg.Retention.Seconds())))
	if err != nil {
		if ctx.Err() == nil {
			r.log.WarnContext(ctx, "outbox cleanup failed", slog.Any("error", err))
		}
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		r.log.DebugContext(ctx, "outbox cleanup", slog.Int64("deleted", n))
	}
}
