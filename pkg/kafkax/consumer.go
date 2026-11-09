package kafkax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/zahra-ove/ticketing/pkg/messaging"
)

// Handler processes one record. It must be idempotent: delivery is
// at-least-once, so a record can arrive more than once.
type Handler func(ctx context.Context, rec *kgo.Record) error

// permanentError marks a failure that a retry cannot fix.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent wraps err so the consumer sends the record to the dead-letter
// topic right away instead of retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p) || errors.Is(err, messaging.ErrMalformed)
}

// ConsumerConfig configures a Consumer.
type ConsumerConfig struct {
	Brokers string
	Group   string
	Topics  []string
	// MaxAttempts is how many times a record is tried before it goes to the
	// dead-letter topic. Defaults to 5.
	MaxAttempts int
	// InitialBackoff is the wait before the first retry; it doubles each time.
	// Defaults to 200ms.
	InitialBackoff time.Duration
}

// Consumer reads records of a consumer group and hands them to a Handler.
//
// Offsets are committed only after a record was handled (or dead-lettered),
// which gives at-least-once delivery. Records of one partition are handled in
// order. A record that keeps failing is moved to "<topic>.dlq" with the error
// in a header, so one bad message cannot block its partition forever.
type Consumer struct {
	client  *kgo.Client
	tracer  *kotel.Tracer
	handler Handler
	log     *slog.Logger
	cfg     ConsumerConfig
}

// NewConsumer joins the consumer group and returns a Consumer.
func NewConsumer(cfg ConsumerConfig, h Handler, log *slog.Logger) (*Consumer, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 200 * time.Millisecond
	}

	tracer := kotel.NewTracer(kotel.ConsumerGroup(cfg.Group))
	k := kotel.NewKotel(kotel.WithTracer(tracer), kotel.WithMeter(kotel.NewMeter()))

	client, err := kgo.NewClient(
		kgo.SeedBrokers(splitBrokers(cfg.Brokers)...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.ClientID(cfg.Group),
		kgo.DisableAutoCommit(),
		// A new group starts from the oldest record, so nothing published
		// before the first deployment is missed.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.RequiredAcks(kgo.AllISRAcks()), // for dead-letter writes
		kgo.WithHooks(k.Hooks()...),
	)
	if err != nil {
		return nil, fmt.Errorf("create kafka consumer: %w", err)
	}
	return &Consumer{client: client, tracer: tracer, handler: h, log: log, cfg: cfg}, nil
}

// Run consumes until ctx is cancelled, then leaves the group cleanly.
func (c *Consumer) Run(ctx context.Context) error {
	defer c.client.Close()
	c.log.InfoContext(ctx, "kafka consumer started", slog.String("group", c.cfg.Group), slog.Any("topics", c.cfg.Topics))

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "kafka consumer stopping", slog.String("group", c.cfg.Group))
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			c.log.ErrorContext(ctx, "kafka fetch failed",
				slog.String("topic", topic), slog.Int("partition", int(partition)), slog.Any("error", err))
		})

		var stopped bool
		fetches.EachRecord(func(rec *kgo.Record) {
			if stopped {
				return
			}
			if err := c.process(ctx, rec); err != nil {
				// Only happens on shutdown: leave the offset uncommitted so the
				// record is redelivered to whoever owns the partition next.
				stopped = true
				return
			}
			c.client.MarkCommitRecords(rec)
		})

		if err := c.client.CommitMarkedOffsets(context.WithoutCancel(ctx)); err != nil {
			c.log.ErrorContext(ctx, "kafka commit failed", slog.Any("error", err))
		}
		if stopped {
			return nil
		}
	}
}

// process handles one record with retries. It returns an error only when ctx
// was cancelled before the record could be handled or dead-lettered.
func (c *Consumer) process(ctx context.Context, rec *kgo.Record) error {
	// The process span continues the producer's trace (from the record
	// headers). It is re-attached to ctx so that shutdown still cancels the handler.
	_, span := c.tracer.WithProcessSpan(rec)
	defer span.End()
	pctx := trace.ContextWithSpan(ctx, span)

	log := c.log.With(
		slog.String("topic", rec.Topic),
		slog.Int("partition", int(rec.Partition)),
		slog.Int64("offset", rec.Offset),
		slog.String("type", header(rec, messaging.HeaderType)),
	)

	backoff := c.cfg.InitialBackoff
	var err error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if err = c.handler(pctx, rec); err == nil {
			return nil
		}
		if isPermanent(err) {
			break
		}
		log.WarnContext(pctx, "handling message failed; will retry",
			slog.Int("attempt", attempt), slog.Duration("backoff", backoff), slog.Any("error", err))
		if attempt == c.cfg.MaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	log.ErrorContext(pctx, "moving message to dead-letter topic", slog.Any("error", err))
	return c.deadLetter(pctx, rec, err)
}

func (c *Consumer) deadLetter(ctx context.Context, rec *kgo.Record, cause error) error {
	dlq := &kgo.Record{
		Topic: rec.Topic + ".dlq",
		Key:   rec.Key,
		Value: rec.Value,
		Headers: append(append([]kgo.RecordHeader{}, rec.Headers...),
			kgo.RecordHeader{Key: "dlq_error", Value: []byte(cause.Error())},
			kgo.RecordHeader{Key: "dlq_consumer_group", Value: []byte(c.cfg.Group)},
			kgo.RecordHeader{Key: "dlq_original_offset", Value: []byte(strconv.FormatInt(rec.Offset, 10))},
		),
	}
	for {
		err := c.client.ProduceSync(ctx, dlq).FirstErr()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log.ErrorContext(ctx, "writing to dead-letter topic failed; retrying", slog.Any("error", err))
		time.Sleep(time.Second)
	}
}

func header(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// Header returns the value of a record header, or "".
func Header(rec *kgo.Record, key string) string { return header(rec, key) }
