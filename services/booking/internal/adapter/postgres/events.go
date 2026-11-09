package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	bookingv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/booking/v1"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/pkg/outbox"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

const source = "booking"

// saveEvents translates domain events into the public contract and stores
// them in the outbox, inside the caller's transaction.
func saveEvents(ctx context.Context, tx pgx.Tx, events []domain.Event) error {
	for _, e := range events {
		payload, at, err := toContract(e)
		if err != nil {
			return err
		}
		msg, err := messaging.New(messaging.TopicBookingEvents, e.BookingID().String(), source, payload, at.AsTime())
		if err != nil {
			return err
		}
		if err := outbox.Add(ctx, tx, msg); err != nil {
			return err
		}
	}
	return nil
}

func toContract(e domain.Event) (proto.Message, *timestamppb.Timestamp, error) {
	switch e := e.(type) {
	case domain.BookingCreated:
		at := timestamppb.New(e.At)
		return &bookingv1.BookingCreated{
			BookingId:   e.Booking.ID.String(),
			EventId:     e.Booking.EventID.String(),
			CustomerId:  e.Booking.CustomerID.String(),
			AmountCents: e.Booking.TotalCents,
			SeatIds:     seatIDStrings(e.Booking.Seats),
			CreatedAt:   at,
		}, at, nil
	case domain.BookingConfirmed:
		at := timestamppb.New(e.At)
		return &bookingv1.BookingConfirmed{
			BookingId:   e.Booking.ID.String(),
			EventId:     e.Booking.EventID.String(),
			CustomerId:  e.Booking.CustomerID.String(),
			AmountCents: e.Booking.TotalCents,
			ConfirmedAt: at,
		}, at, nil
	case domain.BookingCancelled:
		at := timestamppb.New(e.At)
		return &bookingv1.BookingCancelled{
			BookingId:   e.Booking.ID.String(),
			EventId:     e.Booking.EventID.String(),
			CustomerId:  e.Booking.CustomerID.String(),
			Reason:      toContractReason(e.Reason),
			CancelledAt: at,
		}, at, nil
	case domain.RefundRequested:
		at := timestamppb.New(e.At)
		return &bookingv1.RefundRequested{
			BookingId:   e.Booking.ID.String(),
			CustomerId:  e.Booking.CustomerID.String(),
			PaymentId:   e.PaymentID.String(),
			RequestedAt: at,
		}, at, nil
	default:
		return nil, nil, fmt.Errorf("no contract for domain event %T", e)
	}
}

func toContractReason(r domain.CancelReason) bookingv1.CancelReason {
	switch r {
	case domain.CancelByCustomer:
		return bookingv1.CancelReason_CANCEL_REASON_CUSTOMER
	case domain.CancelPaymentFailed:
		return bookingv1.CancelReason_CANCEL_REASON_PAYMENT_FAILED
	case domain.CancelPaymentTimeout:
		return bookingv1.CancelReason_CANCEL_REASON_PAYMENT_TIMEOUT
	default:
		return bookingv1.CancelReason_CANCEL_REASON_UNSPECIFIED
	}
}

func seatIDStrings(seats []domain.Seat) []string {
	out := make([]string, len(seats))
	for i, s := range seats {
		out[i] = s.ID.String()
	}
	return out
}

// nullableReason maps an empty reason to SQL NULL.
func nullableReason(r domain.CancelReason) *string {
	if r == "" {
		return nil
	}
	s := string(r)
	return &s
}
