// Package consumer turns booking events from Kafka into payment use cases.
package consumer

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	bookingv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/booking/v1"
	"github.com/zahra-ove/ticketing/pkg/kafkax"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

// Service is the part of the application layer the consumer drives.
type Service interface {
	ChargeBooking(ctx context.Context, c domain.Charge) (*domain.Payment, bool, error)
	RefundBooking(ctx context.Context, bookingID uuid.UUID) error
}

var (
	typeBookingCreated  = messaging.TypeOf(&bookingv1.BookingCreated{})
	typeRefundRequested = messaging.TypeOf(&bookingv1.RefundRequested{})
)

// NewHandler returns the Kafka handler for the booking events topic.
func NewHandler(svc Service) kafkax.Handler {
	return func(ctx context.Context, rec *kgo.Record) error {
		switch kafkax.Header(rec, messaging.HeaderType) {
		case typeBookingCreated:
			var ev bookingv1.BookingCreated
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			bookingID, err1 := uuid.Parse(ev.GetBookingId())
			customerID, err2 := uuid.Parse(ev.GetCustomerId())
			if err1 != nil || err2 != nil {
				return kafkax.Permanent(fmt.Errorf("BookingCreated with invalid ids: %q, %q", ev.GetBookingId(), ev.GetCustomerId()))
			}
			_, _, err := svc.ChargeBooking(ctx, domain.Charge{BookingID: bookingID, CustomerID: customerID, AmountCents: ev.GetAmountCents()})
			return err

		case typeRefundRequested:
			var ev bookingv1.RefundRequested
			if err := messaging.Decode(rec.Value, &ev); err != nil {
				return err
			}
			bookingID, err := uuid.Parse(ev.GetBookingId())
			if err != nil {
				return kafkax.Permanent(fmt.Errorf("RefundRequested with invalid booking id %q", ev.GetBookingId()))
			}
			return svc.RefundBooking(ctx, bookingID)

		default:
			// Other booking events (confirmed, cancelled) are not the payment service's business.
			return nil
		}
	}
}
