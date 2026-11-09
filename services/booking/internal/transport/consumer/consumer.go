// Package consumer turns payment events from Kafka into booking saga steps.
package consumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	paymentv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/payment/v1"
	"github.com/zahra-ove/ticketing/pkg/kafkax"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// Service is the part of the application layer the consumer drives.
type Service interface {
	HandlePaymentSucceeded(ctx context.Context, bookingID, paymentID uuid.UUID) error
	HandlePaymentFailed(ctx context.Context, bookingID uuid.UUID) error
}

var (
	typePaymentSucceeded = messaging.TypeOf(&paymentv1.PaymentSucceeded{})
	typePaymentFailed    = messaging.TypeOf(&paymentv1.PaymentFailed{})
)

// NewHandler returns the Kafka handler for the payment events topic.
func NewHandler(svc Service) kafkax.Handler {
	return func(ctx context.Context, rec *kgo.Record) error {
		var err error
		switch kafkax.Header(rec, messaging.HeaderType) {
		case typePaymentSucceeded:
			var ev paymentv1.PaymentSucceeded
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			bookingID, err1 := uuid.Parse(ev.GetBookingId())
			paymentID, err2 := uuid.Parse(ev.GetPaymentId())
			if err1 != nil || err2 != nil {
				return kafkax.Permanent(fmt.Errorf("PaymentSucceeded with invalid ids: %q, %q", ev.GetBookingId(), ev.GetPaymentId()))
			}
			err = svc.HandlePaymentSucceeded(ctx, bookingID, paymentID)

		case typePaymentFailed:
			var ev paymentv1.PaymentFailed
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			bookingID, perr := uuid.Parse(ev.GetBookingId())
			if perr != nil {
				return kafkax.Permanent(fmt.Errorf("PaymentFailed with invalid booking id %q", ev.GetBookingId()))
			}
			err = svc.HandlePaymentFailed(ctx, bookingID)

		default:
			return nil // PaymentRefunded and future events need no action here
		}

		// A payment for a booking we do not know cannot be fixed by retrying.
		if errors.Is(err, domain.ErrBookingNotFound) {
			return kafkax.Permanent(err)
		}
		return err
	}
}
