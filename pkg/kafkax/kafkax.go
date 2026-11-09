// Package kafkax builds Kafka clients and consumers on top of franz-go, with
// OpenTelemetry tracing that follows a message from producer to consumer.
package kafkax

import (
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
)

// newKotel returns tracing and metrics hooks that use the global providers
// and the global (W3C trace context) propagator.
func newKotel(group string) *kotel.Kotel {
	tracerOpts := []kotel.TracerOpt{}
	if group != "" {
		tracerOpts = append(tracerOpts, kotel.ConsumerGroup(group))
	}
	return kotel.NewKotel(
		kotel.WithTracer(kotel.NewTracer(tracerOpts...)),
		kotel.WithMeter(kotel.NewMeter()),
	)
}

// NewProducer returns a client for producing. Records are acknowledged by all
// in-sync replicas and the producer is idempotent, so retries never duplicate
// or reorder records within a partition.
func NewProducer(brokers string, clientID string) (*kgo.Client, error) {
	k := newKotel("")
	client, err := kgo.NewClient(
		kgo.SeedBrokers(splitBrokers(brokers)...),
		kgo.ClientID(clientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.WithHooks(k.Hooks()...),
	)
	if err != nil {
		return nil, fmt.Errorf("create kafka producer: %w", err)
	}
	return client, nil
}

func splitBrokers(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
