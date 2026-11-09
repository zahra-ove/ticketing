// Package messaging defines the message envelope shared by all services.
//
// Payloads are protobuf messages encoded with protojson, so they are typed
// for producers and consumers and still readable in the Redpanda console.
// Metadata follows the CloudEvents Kafka binding (binary mode): each
// attribute is a "ce_" header, the payload is the record value.
package messaging

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// CloudEvents header names and the content type of every payload.
const (
	HeaderID          = "ce_id"
	HeaderType        = "ce_type"
	HeaderSource      = "ce_source"
	HeaderTime        = "ce_time"
	HeaderSpecVersion = "ce_specversion"
	HeaderContentType = "content-type"

	ContentTypeJSON = "application/json"
	specVersion     = "1.0"
)

// Topics. Every message of a topic is keyed by booking id, so all events of
// one booking land on the same partition and keep their order.
const (
	TopicBookingEvents = "ticketing.booking.events.v1"
	TopicPaymentEvents = "ticketing.payment.events.v1"
)

// Message is an event ready to be stored in an outbox or produced to Kafka.
type Message struct {
	ID      uuid.UUID
	Topic   string
	Key     string
	Type    string
	Payload []byte
	// Headers carry the CloudEvents attributes and the trace context.
	Headers map[string]string
}

// New encodes payload as a message for topic. source names the producing
// service, e.g. "booking".
func New(topic, key, source string, payload proto.Message, at time.Time) (Message, error) {
	b, err := protojson.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("encode %T: %w", payload, err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate message id: %w", err)
	}
	typ := TypeOf(payload)
	return Message{
		ID:      id,
		Topic:   topic,
		Key:     key,
		Type:    typ,
		Payload: b,
		Headers: map[string]string{
			HeaderID:          id.String(),
			HeaderType:        typ,
			HeaderSource:      source,
			HeaderTime:        at.UTC().Format(time.RFC3339Nano),
			HeaderSpecVersion: specVersion,
			HeaderContentType: ContentTypeJSON,
		},
	}, nil
}

// TypeOf returns the event type of a payload: its fully-qualified protobuf
// name, e.g. "ticketing.booking.v1.BookingCreated".
func TypeOf(m proto.Message) string {
	return string(m.ProtoReflect().Descriptor().FullName())
}

// ErrMalformed marks a payload that cannot be decoded. Retrying will not help.
var ErrMalformed = errors.New("malformed message")

// Decode parses payload into dst. Unknown fields are ignored, so producers
// can add fields without breaking older consumers.
func Decode(payload []byte, dst proto.Message) error {
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrMalformed, TypeOf(dst), err)
	}
	return nil
}
