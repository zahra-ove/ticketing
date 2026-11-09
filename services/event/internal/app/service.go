// Package app holds the use cases of the event service. It orchestrates the
// domain model and talks to storage only through the Repository interface,
// which is defined here, by its consumer.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// Pagination bounds for listing events.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Repository persists events and their seats.
type Repository interface {
	// Create stores a new event together with its seats atomically.
	Create(ctx context.Context, ev *domain.Event, seats []domain.Seat) error
	// Get returns the event or domain.ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*domain.Event, error)
	// ListPublished returns published events ordered by start time, after the cursor.
	ListPublished(ctx context.Context, after *Cursor, limit int) ([]domain.Event, error)
	// Update loads the event, applies fn and saves the result in one transaction.
	// The row is locked while fn runs, so concurrent updates cannot overwrite each other.
	Update(ctx context.Context, id uuid.UUID, fn func(ev *domain.Event) error) (*domain.Event, error)
	// ListSeats returns all seats of an event in venue order: by section (as
	// defined on the event), then row, then seat number.
	ListSeats(ctx context.Context, eventID uuid.UUID) ([]domain.Seat, error)
	// GetSeats returns the seats of the event whose ids are in ids; unknown ids are skipped.
	GetSeats(ctx context.Context, eventID uuid.UUID, ids []uuid.UUID) ([]domain.Seat, error)
}

// MaxSeatsPerLookup bounds GetSeats so a single call cannot load a whole venue.
const MaxSeatsPerLookup = 100

// SeatLookup is the result of GetSeats.
type SeatLookup struct {
	Event *domain.Event
	Seats []domain.Seat
	// Missing lists requested ids that are not seats of the event.
	Missing []uuid.UUID
}

// Service implements the event catalog use cases.
type Service struct {
	repo Repository
	now  func() time.Time
}

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the clock, which is useful in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService returns a Service backed by repo.
func NewService(repo Repository, opts ...Option) *Service {
	s := &Service{repo: repo, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateEvent validates the input, creates a draft event and generates its seats.
func (s *Service) CreateEvent(ctx context.Context, p domain.NewEventParams) (*domain.Event, error) {
	ev, err := domain.NewEvent(p, s.now())
	if err != nil {
		return nil, err
	}

	seats, err := ev.GenerateSeats()
	if err != nil {
		return nil, err
	}

	if err := s.repo.Create(ctx, ev, seats); err != nil {
		return nil, fmt.Errorf("create event: %w", err)
	}
	return ev, nil
}

// GetEvent returns a single event.
func (s *Service) GetEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	ev, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get event %s: %w", id, err)
	}
	return ev, nil
}

// Page is one page of a cursor-paginated list.
type Page struct {
	Events []domain.Event
	// NextCursor is empty on the last page.
	NextCursor string
}

// ListPublishedEvents returns published events ordered by start time.
// cursor is the opaque NextCursor of the previous page, or empty for the first page.
func (s *Service) ListPublishedEvents(ctx context.Context, cursor string, limit int) (Page, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, MaxPageSize)

	after, err := DecodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}

	// Fetch one extra row to know whether another page exists.
	events, err := s.repo.ListPublished(ctx, after, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("list published events: %w", err)
	}

	page := Page{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		last := page.Events[limit-1]
		page.NextCursor = Cursor{StartsAt: last.StartsAt, ID: last.ID}.Encode()
	}
	return page, nil
}

// PublishEvent makes a draft event visible to customers.
func (s *Service) PublishEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	ev, err := s.repo.Update(ctx, id, func(ev *domain.Event) error {
		return ev.Publish(s.now())
	})
	if err != nil {
		return nil, fmt.Errorf("publish event %s: %w", id, err)
	}
	return ev, nil
}

// CancelEvent cancels an event.
func (s *Service) CancelEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	ev, err := s.repo.Update(ctx, id, func(ev *domain.Event) error {
		return ev.Cancel(s.now())
	})
	if err != nil {
		return nil, fmt.Errorf("cancel event %s: %w", id, err)
	}
	return ev, nil
}

// GetSeats returns an event and some of its seats. It serves other services
// (booking) that need prices and labels of the seats a customer picked.
func (s *Service) GetSeats(ctx context.Context, eventID uuid.UUID, seatIDs []uuid.UUID) (SeatLookup, error) {
	if len(seatIDs) == 0 || len(seatIDs) > MaxSeatsPerLookup {
		return SeatLookup{}, &domain.ValidationError{Fields: map[string]string{
			"seat_ids": fmt.Sprintf("must contain between 1 and %d ids", MaxSeatsPerLookup),
		}}
	}

	ev, err := s.repo.Get(ctx, eventID)
	if err != nil {
		return SeatLookup{}, fmt.Errorf("get event %s: %w", eventID, err)
	}

	seats, err := s.repo.GetSeats(ctx, eventID, seatIDs)
	if err != nil {
		return SeatLookup{}, fmt.Errorf("get seats of event %s: %w", eventID, err)
	}

	found := make(map[uuid.UUID]bool, len(seats))
	for _, st := range seats {
		found[st.ID] = true
	}
	var missing []uuid.UUID
	for _, id := range seatIDs {
		if !found[id] {
			missing = append(missing, id)
		}
	}

	return SeatLookup{Event: ev, Seats: seats, Missing: missing}, nil
}

// ListSeats returns the seat map of an event.
func (s *Service) ListSeats(ctx context.Context, eventID uuid.UUID) ([]domain.Seat, error) {
	// Check existence first so that an unknown event is a 404, not an empty list.
	if _, err := s.repo.Get(ctx, eventID); err != nil {
		return nil, fmt.Errorf("get event %s: %w", eventID, err)
	}
	seats, err := s.repo.ListSeats(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("list seats of event %s: %w", eventID, err)
	}
	return seats, nil
}
