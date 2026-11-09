// Package consumer turns booking and payment events into notifications.
package consumer

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	bookingv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/booking/v1"
	paymentv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/payment/v1"
	"github.com/zahra-ove/ticketing/pkg/kafkax"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/services/notification/internal/app"
)

// Service is the part of the application layer the consumer drives.
type Service interface {
	NotifyBookingConfirmed(ctx context.Context, messageID uuid.UUID, d app.BookingConfirmed) error
	NotifyBookingCancelled(ctx context.Context, messageID uuid.UUID, d app.BookingCancelled) error
	NotifyPaymentRefunded(ctx context.Context, messageID uuid.UUID, d app.PaymentRefunded) error
}

var (
	typeBookingConfirmed = messaging.TypeOf(&bookingv1.BookingConfirmed{})
	typeBookingCancelled = messaging.TypeOf(&bookingv1.BookingCancelled{})
	typePaymentRefunded  = messaging.TypeOf(&paymentv1.PaymentRefunded{})
)

// NewHandler returns the Kafka handler for the booking and payment topics.
func NewHandler(svc Service) kafkax.Handler {
	return func(ctx context.Context, rec *kgo.Record) error {
		typ := kafkax.Header(rec, messaging.HeaderType)
		if typ != typeBookingConfirmed && typ != typeBookingCancelled && typ != typePaymentRefunded {
			return nil // not a notification trigger
		}

		// The CloudEvents id identifies the message across redeliveries.
		msgID, err := uuid.Parse(kafkax.Header(rec, messaging.HeaderID))
		if err != nil {
			return kafkax.Permanent(fmt.Errorf("message without a valid %s header", messaging.HeaderID))
		}

		switch typ {
		case typeBookingConfirmed:
			var ev bookingv1.BookingConfirmed
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			ids, err := parseIDs(ev.GetBookingId(), ev.GetCustomerId())
			if err != nil {
				return err
			}
			return svc.NotifyBookingConfirmed(ctx, msgID, app.BookingConfirmed{
				BookingID: ids[0], CustomerID: ids[1], AmountCents: ev.GetAmountCents(), ConfirmedAt: ev.GetConfirmedAt().AsTime(),
			})

		case typeBookingCancelled:
			var ev bookingv1.BookingCancelled
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			ids, err := parseIDs(ev.GetBookingId(), ev.GetCustomerId())
			if err != nil {
				return err
			}
			return svc.NotifyBookingCancelled(ctx, msgID, app.BookingCancelled{
				BookingID: ids[0], CustomerID: ids[1], Reason: reason(ev.GetReason()),
			})

		default: // typePaymentRefunded
			var ev paymentv1.PaymentRefunded
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			ids, err := parseIDs(ev.GetBookingId(), ev.GetCustomerId())
			if err != nil {
				return err
			}
			return svc.NotifyPaymentRefunded(ctx, msgID, app.PaymentRefunded{
				BookingID: ids[0], CustomerID: ids[1], AmountCents: ev.GetAmountCents(),
			})
		}
	}
}

// reason turns CANCEL_REASON_PAYMENT_TIMEOUT into "payment_timeout".
func reason(r bookingv1.CancelReason) string {
	return strings.ToLower(strings.TrimPrefix(r.String(), "CANCEL_REASON_"))
}

func parseIDs(raw ...string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, len(raw))
	for i, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, kafkax.Permanent(fmt.Errorf("invalid id %q: %w", s, err))
		}
		out[i] = id
	}
	return out, nil
}
