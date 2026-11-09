// Package domain contains the booking model and its business rules: seat
// holds that expire, and bookings created from a hold.
package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MaxSeatsPerHold limits how many seats one customer can hold at once.
const MaxSeatsPerHold = 10

// Seat is a seat as priced at the moment it was held. The price is locked for
// the lifetime of the hold, so a price change does not surprise the customer.
type Seat struct {
	ID         uuid.UUID
	Label      string
	PriceCents int64
}

// Hold reserves seats for a customer for a short time while they pay.
// Holds live only in Redis and disappear on expiry.
type Hold struct {
	ID         uuid.UUID
	EventID    uuid.UUID
	CustomerID uuid.UUID
	Seats      []Seat
	ExpiresAt  time.Time
}

// TotalCents is the sum of the seat prices.
func (h *Hold) TotalCents() int64 {
	var total int64
	for _, s := range h.Seats {
		total += s.PriceCents
	}
	return total
}

// SeatIDs returns the ids of the held seats.
func (h *Hold) SeatIDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(h.Seats))
	for i, s := range h.Seats {
		ids[i] = s.ID
	}
	return ids
}

// ValidateSeatSelection checks the seat ids a customer asked to hold.
func ValidateSeatSelection(seatIDs []uuid.UUID) error {
	if len(seatIDs) == 0 || len(seatIDs) > MaxSeatsPerHold {
		return NewValidationError("seat_ids", fmt.Sprintf("must contain between 1 and %d seats", MaxSeatsPerHold))
	}
	seen := make(map[uuid.UUID]bool, len(seatIDs))
	for _, id := range seatIDs {
		if seen[id] {
			return NewValidationError("seat_ids", "must not contain duplicates")
		}
		seen[id] = true
	}
	return nil
}

// Status is the lifecycle state of a booking.
type Status string

// Booking lifecycle: pending_payment -> confirmed, or pending_payment -> cancelled.
const (
	StatusPendingPayment Status = "pending_payment"
	StatusConfirmed      Status = "confirmed"
	StatusCancelled      Status = "cancelled"
)

// Booking is a customer's claim on seats. While it is pending or confirmed,
// its seats cannot be booked by anyone else.
type Booking struct {
	ID         uuid.UUID
	HoldID     uuid.UUID
	EventID    uuid.UUID
	CustomerID uuid.UUID
	Status     Status
	Seats      []Seat
	TotalCents int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewBooking turns a live hold into a booking that waits for payment.
func NewBooking(h *Hold, now time.Time) (*Booking, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate booking id: %w", err)
	}
	now = now.UTC()
	return &Booking{
		ID:         id,
		HoldID:     h.ID,
		EventID:    h.EventID,
		CustomerID: h.CustomerID,
		Status:     StatusPendingPayment,
		Seats:      h.Seats,
		TotalCents: h.TotalCents(),
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Active reports whether the booking still blocks its seats.
func (b *Booking) Active() bool { return b.Status != StatusCancelled }

// Cancel releases the seats of a booking that has not been paid yet.
func (b *Booking) Cancel(now time.Time) error {
	if b.Status != StatusPendingPayment {
		return fmt.Errorf("%w: cannot cancel a %s booking", ErrInvalidTransition, b.Status)
	}
	b.Status = StatusCancelled
	b.UpdatedAt = now.UTC()
	return nil
}

// Confirm marks a pending booking as paid.
func (b *Booking) Confirm(now time.Time) error {
	if b.Status != StatusPendingPayment {
		return fmt.Errorf("%w: cannot confirm a %s booking", ErrInvalidTransition, b.Status)
	}
	b.Status = StatusConfirmed
	b.UpdatedAt = now.UTC()
	return nil
}
