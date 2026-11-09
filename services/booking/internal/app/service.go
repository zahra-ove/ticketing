// Package app holds the use cases of the booking service.
//
// The flow is: a customer holds seats (Redis, short TTL), then turns the hold
// into a booking (Postgres). Redis gives fast, atomic, self-expiring holds;
// Postgres is the source of truth and its unique index is the final guard
// against selling a seat twice.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

// DefaultHoldTTL is how long seats stay held when no TTL is configured.
const DefaultHoldTTL = 10 * time.Minute

// Service implements the booking use cases.
type Service struct {
	catalog Catalog
	holds   HoldStore
	repo    Repository
	holdTTL time.Duration
	now     func() time.Time
	log     *slog.Logger
}

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the clock, which is useful in tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithHoldTTL sets how long seats stay held.
func WithHoldTTL(ttl time.Duration) Option { return func(s *Service) { s.holdTTL = ttl } }

// WithLogger sets the logger for problems that do not fail a request.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// NewService returns a Service.
func NewService(catalog Catalog, holds HoldStore, repo Repository, opts ...Option) *Service {
	s := &Service{
		catalog: catalog,
		holds:   holds,
		repo:    repo,
		holdTTL: DefaultHoldTTL,
		now:     time.Now,
		log:     slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// HoldSeats holds seats of an event for a customer. Prices are read from the
// catalog and locked into the hold.
func (s *Service) HoldSeats(ctx context.Context, customerID, eventID uuid.UUID, seatIDs []uuid.UUID) (*domain.Hold, error) {
	if err := domain.ValidateSeatSelection(seatIDs); err != nil {
		return nil, err
	}

	res, err := s.catalog.GetSeats(ctx, eventID, seatIDs)
	if err != nil {
		return nil, fmt.Errorf("look up seats: %w", err)
	}
	if len(res.Missing) > 0 {
		return nil, domain.NewValidationError("seat_ids", "unknown seats for this event: "+joinIDs(res.Missing))
	}
	now := s.now()
	if !res.Event.Published || !res.Event.StartsAt.After(now) {
		return nil, domain.ErrEventNotBookable
	}

	// Cheap early check against sold seats. It is not a guarantee (a seat can
	// be booked right after this read); the unique index at booking time is.
	booked, err := s.repo.BookedSeats(ctx, eventID, seatIDs)
	if err != nil {
		return nil, fmt.Errorf("check booked seats: %w", err)
	}
	if len(booked) > 0 {
		return nil, &domain.SeatsUnavailableError{SeatIDs: booked}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate hold id: %w", err)
	}
	hold := &domain.Hold{
		ID:         id,
		EventID:    eventID,
		CustomerID: customerID,
		Seats:      res.Seats,
		ExpiresAt:  now.Add(s.holdTTL).UTC(),
	}
	if err := s.holds.Hold(ctx, hold, s.holdTTL); err != nil {
		return nil, fmt.Errorf("hold seats: %w", err)
	}
	return hold, nil
}

// GetHold returns a hold of the customer.
func (s *Service) GetHold(ctx context.Context, customerID, holdID uuid.UUID) (*domain.Hold, error) {
	h, err := s.holds.Get(ctx, holdID)
	if err != nil {
		return nil, fmt.Errorf("get hold %s: %w", holdID, err)
	}
	// Someone else's hold is reported as missing, so ids cannot be probed.
	if h.CustomerID != customerID {
		return nil, domain.ErrHoldNotFound
	}
	return h, nil
}

// ReleaseHold gives the seats of a hold back before it expires.
func (s *Service) ReleaseHold(ctx context.Context, customerID, holdID uuid.UUID) error {
	h, err := s.GetHold(ctx, customerID, holdID)
	if err != nil {
		return err
	}
	if err := s.holds.Release(ctx, h); err != nil {
		return fmt.Errorf("release hold %s: %w", holdID, err)
	}
	return nil
}

// CreateBooking turns a hold into a booking that waits for payment.
//
// It is idempotent per hold: retrying with the same hold returns the booking
// created by the first call, with created=false. Clients can therefore retry
// safely after a timeout without booking twice.
func (s *Service) CreateBooking(ctx context.Context, customerID, holdID uuid.UUID) (b *domain.Booking, created bool, err error) {
	if b, err := s.existingBooking(ctx, customerID, holdID); err == nil || !errors.Is(err, domain.ErrBookingNotFound) {
		return b, false, err
	}

	h, err := s.GetHold(ctx, customerID, holdID)
	if err != nil {
		return nil, false, err
	}
	// The hold record can outlive its seat keys by a few milliseconds; make
	// sure we still own every seat before we book.
	if err := s.holds.Verify(ctx, h); err != nil {
		return nil, false, fmt.Errorf("verify hold %s: %w", holdID, err)
	}

	b, err = domain.NewBooking(h, s.now())
	if err != nil {
		return nil, false, err
	}

	switch err := s.repo.Create(ctx, b); {
	case errors.Is(err, ErrHoldAlreadyUsed):
		// A concurrent retry with the same hold won the race; return its booking.
		existing, err := s.existingBooking(ctx, customerID, holdID)
		return existing, false, err
	case err != nil:
		return nil, false, fmt.Errorf("create booking: %w", err)
	}

	// The seats are now protected by the database. Releasing the hold only
	// tidies Redis; if it fails the keys simply expire, so it is not an error.
	if err := s.holds.Release(context.WithoutCancel(ctx), h); err != nil {
		s.log.WarnContext(ctx, "release hold after booking failed",
			slog.String("hold_id", h.ID.String()), slog.Any("error", err))
	}
	return b, true, nil
}

func (s *Service) existingBooking(ctx context.Context, customerID, holdID uuid.UUID) (*domain.Booking, error) {
	b, err := s.repo.GetByHoldID(ctx, holdID)
	if err != nil {
		return nil, err
	}
	if b.CustomerID != customerID {
		return nil, domain.ErrHoldNotFound
	}
	return b, nil
}

// GetBooking returns a booking of the customer.
func (s *Service) GetBooking(ctx context.Context, customerID, id uuid.UUID) (*domain.Booking, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get booking %s: %w", id, err)
	}
	if b.CustomerID != customerID {
		return nil, domain.ErrBookingNotFound
	}
	return b, nil
}

// CancelBooking cancels an unpaid booking and frees its seats.
func (s *Service) CancelBooking(ctx context.Context, customerID, id uuid.UUID) (*domain.Booking, error) {
	b, err := s.repo.Update(ctx, id, func(b *domain.Booking) error {
		if b.CustomerID != customerID {
			return domain.ErrBookingNotFound
		}
		return b.Cancel(s.now())
	})
	if err != nil {
		return nil, fmt.Errorf("cancel booking %s: %w", id, err)
	}
	return b, nil
}

// Availability lists the seats of an event that cannot be held right now.
type Availability struct {
	Booked []uuid.UUID
	Held   []uuid.UUID
}

// GetAvailability returns the booked and held seats of an event.
func (s *Service) GetAvailability(ctx context.Context, eventID uuid.UUID) (Availability, error) {
	booked, err := s.repo.BookedSeats(ctx, eventID, nil)
	if err != nil {
		return Availability{}, fmt.Errorf("list booked seats: %w", err)
	}
	held, err := s.holds.HeldSeats(ctx, eventID)
	if err != nil {
		return Availability{}, fmt.Errorf("list held seats: %w", err)
	}
	return Availability{Booked: booked, Held: held}, nil
}

func joinIDs(ids []uuid.UUID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = id.String()
	}
	return strings.Join(s, ", ")
}
