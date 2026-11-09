package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// The interfaces below are the outbound ports of the booking service. They are
// defined here, by their consumer, and implemented by the adapters.

// CatalogEvent is what the booking service needs to know about an event.
type CatalogEvent struct {
	Published bool
	StartsAt  time.Time
}

// CatalogResult is the answer of the event catalog for a set of seats.
type CatalogResult struct {
	Event   CatalogEvent
	Seats   []domain.Seat
	Missing []uuid.UUID
}

// Catalog reads events and seat prices from the event service.
type Catalog interface {
	// GetSeats returns domain.ErrEventNotFound when the event does not exist.
	GetSeats(ctx context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) (CatalogResult, error)
}

// HoldStore keeps short-lived seat holds. Holding is all-or-nothing: either
// every requested seat is held, or none is.
type HoldStore interface {
	// Hold reserves the seats of h until ttl elapses. It returns a
	// *domain.SeatsUnavailableError listing the seats someone else holds.
	Hold(ctx context.Context, h *domain.Hold, ttl time.Duration) error
	// Get returns the hold or domain.ErrHoldNotFound once it expired.
	Get(ctx context.Context, holdID uuid.UUID) (*domain.Hold, error)
	// Verify returns domain.ErrHoldNotFound unless h still owns all its seats.
	Verify(ctx context.Context, h *domain.Hold) error
	// Release frees the seats of h. Seats already re-held by others are left alone.
	Release(ctx context.Context, h *domain.Hold) error
	// HeldSeats lists the seats of an event that are currently held.
	HeldSeats(ctx context.Context, eventID uuid.UUID) ([]uuid.UUID, error)
}

// ErrDependencyUnavailable wraps failures of a downstream service that may
// recover on retry (timeouts, restarts). The API reports them as 503.
var ErrDependencyUnavailable = errors.New("dependency unavailable")

// ErrHoldAlreadyUsed is returned by Repository.Create when a booking for the
// same hold exists. It makes booking creation idempotent.
var ErrHoldAlreadyUsed = errors.New("a booking for this hold already exists")

// Repository persists bookings. The database is the source of truth for sold
// seats: a unique index guarantees a seat is in at most one active booking.
type Repository interface {
	// Create stores a booking. It returns a *domain.SeatsUnavailableError when
	// a seat is already booked, and ErrHoldAlreadyUsed for a reused hold.
	Create(ctx context.Context, b *domain.Booking) error
	// Get returns the booking or domain.ErrBookingNotFound.
	Get(ctx context.Context, id uuid.UUID) (*domain.Booking, error)
	// GetByHoldID returns the booking made from a hold or domain.ErrBookingNotFound.
	GetByHoldID(ctx context.Context, holdID uuid.UUID) (*domain.Booking, error)
	// Update loads the booking, applies fn and saves it in one transaction with the row locked.
	Update(ctx context.Context, id uuid.UUID, fn func(b *domain.Booking) error) (*domain.Booking, error)
	// BookedSeats returns the seats of the event that are in an active booking.
	// When seatIDs is not empty, only those seats are checked.
	BookedSeats(ctx context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) ([]uuid.UUID, error)
}
