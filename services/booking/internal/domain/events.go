package domain

import (
	"time"

	"github.com/google/uuid"
)

// Event is something that happened to a booking. Events are recorded by the
// aggregate and published by the infrastructure through the outbox.
type Event interface {
	// BookingID is used as the message key, so events of a booking stay ordered.
	BookingID() uuid.UUID
}

// BookingCreated starts the payment saga.
type BookingCreated struct {
	Booking Booking
	At      time.Time
}

// BookingConfirmed means the booking is paid.
type BookingConfirmed struct {
	Booking Booking
	At      time.Time
}

// BookingCancelled means the seats of the booking are free again.
type BookingCancelled struct {
	Booking Booking
	Reason  CancelReason
	At      time.Time
}

// RefundRequested asks the payment service to give the money back.
type RefundRequested struct {
	Booking   Booking
	PaymentID uuid.UUID
	At        time.Time
}

// BookingID implements Event.
func (e BookingCreated) BookingID() uuid.UUID { return e.Booking.ID }

// BookingID implements Event.
func (e BookingConfirmed) BookingID() uuid.UUID { return e.Booking.ID }

// BookingID implements Event.
func (e BookingCancelled) BookingID() uuid.UUID { return e.Booking.ID }

// BookingID implements Event.
func (e RefundRequested) BookingID() uuid.UUID { return e.Booking.ID }
