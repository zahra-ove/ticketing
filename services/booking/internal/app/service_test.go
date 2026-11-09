package app_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// fakeCatalog serves one event with a fixed set of seats.
type fakeCatalog struct {
	eventID uuid.UUID
	event   app.CatalogEvent
	seats   map[uuid.UUID]domain.Seat
}

func (c *fakeCatalog) GetSeats(_ context.Context, eventID uuid.UUID, ids []uuid.UUID) (app.CatalogResult, error) {
	if eventID != c.eventID {
		return app.CatalogResult{}, domain.ErrEventNotFound
	}
	res := app.CatalogResult{Event: c.event}
	for _, id := range ids {
		if s, ok := c.seats[id]; ok {
			res.Seats = append(res.Seats, s)
		} else {
			res.Missing = append(res.Missing, id)
		}
	}
	return res, nil
}

// fakeHolds is an in-memory HoldStore without expiry.
type fakeHolds struct {
	mu     sync.Mutex
	holds  map[uuid.UUID]*domain.Hold
	owners map[uuid.UUID]uuid.UUID // seat -> hold
}

func newFakeHolds() *fakeHolds {
	return &fakeHolds{holds: map[uuid.UUID]*domain.Hold{}, owners: map[uuid.UUID]uuid.UUID{}}
}

func (f *fakeHolds) Hold(_ context.Context, h *domain.Hold, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var taken []uuid.UUID
	for _, s := range h.Seats {
		if _, ok := f.owners[s.ID]; ok {
			taken = append(taken, s.ID)
		}
	}
	if len(taken) > 0 {
		return &domain.SeatsUnavailableError{SeatIDs: taken}
	}
	for _, s := range h.Seats {
		f.owners[s.ID] = h.ID
	}
	f.holds[h.ID] = h
	return nil
}

func (f *fakeHolds) Get(_ context.Context, id uuid.UUID) (*domain.Hold, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.holds[id]
	if !ok {
		return nil, domain.ErrHoldNotFound
	}
	return h, nil
}

func (f *fakeHolds) Verify(_ context.Context, h *domain.Hold) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range h.Seats {
		if f.owners[s.ID] != h.ID {
			return domain.ErrHoldNotFound
		}
	}
	return nil
}

func (f *fakeHolds) Release(_ context.Context, h *domain.Hold) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range h.Seats {
		if f.owners[s.ID] == h.ID {
			delete(f.owners, s.ID)
		}
	}
	delete(f.holds, h.ID)
	return nil
}

func (f *fakeHolds) HeldSeats(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []uuid.UUID
	for id := range f.owners {
		ids = append(ids, id)
	}
	return ids, nil
}

// fakeRepo mimics the database guarantees: unique hold id, one active booking per seat.
type fakeRepo struct {
	mu       sync.Mutex
	bookings map[uuid.UUID]*domain.Booking
}

func newFakeRepo() *fakeRepo { return &fakeRepo{bookings: map[uuid.UUID]*domain.Booking{}} }

func (r *fakeRepo) Create(_ context.Context, b *domain.Booking) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.bookings {
		if existing.HoldID == b.HoldID {
			return app.ErrHoldAlreadyUsed
		}
		if !existing.Active() {
			continue
		}
		for _, s := range b.Seats {
			if slices.ContainsFunc(existing.Seats, func(e domain.Seat) bool { return e.ID == s.ID }) {
				return &domain.SeatsUnavailableError{SeatIDs: []uuid.UUID{s.ID}}
			}
		}
	}
	cp := *b
	r.bookings[b.ID] = &cp
	return nil
}

func (r *fakeRepo) Get(_ context.Context, id uuid.UUID) (*domain.Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.bookings[id]
	if !ok {
		return nil, domain.ErrBookingNotFound
	}
	cp := *b
	return &cp, nil
}

func (r *fakeRepo) GetByHoldID(_ context.Context, holdID uuid.UUID) (*domain.Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.bookings {
		if b.HoldID == holdID {
			cp := *b
			return &cp, nil
		}
	}
	return nil, domain.ErrBookingNotFound
}

func (r *fakeRepo) Update(_ context.Context, id uuid.UUID, fn func(*domain.Booking) error) (*domain.Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.bookings[id]
	if !ok {
		return nil, domain.ErrBookingNotFound
	}
	cp := *b
	if err := fn(&cp); err != nil {
		return nil, err
	}
	r.bookings[id] = &cp
	return &cp, nil
}

func (r *fakeRepo) BookedSeats(_ context.Context, eventID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []uuid.UUID
	for _, b := range r.bookings {
		if b.EventID != eventID || !b.Active() {
			continue
		}
		for _, s := range b.Seats {
			if len(ids) == 0 || slices.Contains(ids, s.ID) {
				out = append(out, s.ID)
			}
		}
	}
	return out, nil
}

type fixture struct {
	svc     *app.Service
	catalog *fakeCatalog
	holds   *fakeHolds
	repo    *fakeRepo
	seats   []uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		catalog: &fakeCatalog{
			eventID: uuid.New(),
			event:   app.CatalogEvent{Published: true, StartsAt: now.Add(24 * time.Hour)},
			seats:   map[uuid.UUID]domain.Seat{},
		},
		holds: newFakeHolds(),
		repo:  newFakeRepo(),
	}
	for i := range 4 {
		id := uuid.New()
		f.seats = append(f.seats, id)
		f.catalog.seats[id] = domain.Seat{ID: id, Label: "A-1-" + string(rune('1'+i)), PriceCents: 1000}
	}
	f.svc = app.NewService(f.catalog, f.holds, f.repo, app.WithClock(func() time.Time { return now }))
	return f
}

func TestHoldSeats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(f *fixture)
		seats   func(f *fixture) []uuid.UUID
		eventID func(f *fixture) uuid.UUID
		check   func(t *testing.T, err error)
	}{
		{
			name:  "holds and locks prices",
			seats: func(f *fixture) []uuid.UUID { return f.seats[:2] },
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "unknown event",
			eventID: func(*fixture) uuid.UUID { return uuid.New() },
			check:   wantIs(domain.ErrEventNotFound),
		},
		{
			name:  "unknown seat",
			seats: func(f *fixture) []uuid.UUID { return []uuid.UUID{f.seats[0], uuid.New()} },
			check: wantValidation,
		},
		{
			name:  "draft event",
			setup: func(f *fixture) { f.catalog.event.Published = false },
			check: wantIs(domain.ErrEventNotBookable),
		},
		{
			name:  "event already started",
			setup: func(f *fixture) { f.catalog.event.StartsAt = now.Add(-time.Minute) },
			check: wantIs(domain.ErrEventNotBookable),
		},
		{
			name: "seat already booked",
			setup: func(f *fixture) {
				b, _ := domain.NewBooking(&domain.Hold{ID: uuid.New(), EventID: f.catalog.eventID, Seats: []domain.Seat{{ID: f.seats[0]}}}, now)
				_ = f.repo.Create(context.Background(), b)
			},
			check: wantUnavailable,
		},
		{
			name: "seat held by someone else",
			setup: func(f *fixture) {
				_ = f.holds.Hold(context.Background(), &domain.Hold{ID: uuid.New(), Seats: []domain.Seat{{ID: f.seats[1]}}}, time.Minute)
			},
			check: wantUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			seats, eventID := f.seats[:2], f.catalog.eventID
			if tt.seats != nil {
				seats = tt.seats(f)
			}
			if tt.eventID != nil {
				eventID = tt.eventID(f)
			}

			h, err := f.svc.HoldSeats(t.Context(), uuid.New(), eventID, seats)

			tt.check(t, err)
			if err == nil && (h.TotalCents() != 2000 || !h.ExpiresAt.Equal(now.Add(app.DefaultHoldTTL))) {
				t.Errorf("hold = %+v", h)
			}
		})
	}
}

func TestCreateBooking_IsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	customer := uuid.New()

	h, err := f.svc.HoldSeats(t.Context(), customer, f.catalog.eventID, f.seats[:2])
	if err != nil {
		t.Fatal(err)
	}

	first, created, err := f.svc.CreateBooking(t.Context(), customer, h.ID)
	if err != nil || !created {
		t.Fatalf("first CreateBooking() = %v, created=%v", err, created)
	}
	if first.Status != domain.StatusPendingPayment || first.TotalCents != 2000 {
		t.Errorf("booking = %+v", first)
	}
	if _, err := f.holds.Get(t.Context(), h.ID); !errors.Is(err, domain.ErrHoldNotFound) {
		t.Error("hold should be released after booking")
	}

	// A retry (e.g. after a client timeout) returns the same booking.
	again, created, err := f.svc.CreateBooking(t.Context(), customer, h.ID)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("retry = %v, created=%v, id=%s, want %s", err, created, again.ID, first.ID)
	}
}

func TestCreateBooking_OtherCustomersHold(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	h, err := f.svc.HoldSeats(t.Context(), uuid.New(), f.catalog.eventID, f.seats[:1])
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = f.svc.CreateBooking(t.Context(), uuid.New(), h.ID)
	if !errors.Is(err, domain.ErrHoldNotFound) {
		t.Fatalf("error = %v, want ErrHoldNotFound", err)
	}
}

func TestCancelBooking_FreesSeats(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	customer := uuid.New()

	h, _ := f.svc.HoldSeats(t.Context(), customer, f.catalog.eventID, f.seats[:1])
	b, _, err := f.svc.CreateBooking(t.Context(), customer, h.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.CancelBooking(t.Context(), uuid.New(), b.ID); !errors.Is(err, domain.ErrBookingNotFound) {
		t.Errorf("cancel by other customer error = %v, want ErrBookingNotFound", err)
	}
	if _, err := f.svc.CancelBooking(t.Context(), customer, b.ID); err != nil {
		t.Fatalf("CancelBooking() error = %v", err)
	}
	if _, err := f.svc.HoldSeats(t.Context(), uuid.New(), f.catalog.eventID, f.seats[:1]); err != nil {
		t.Errorf("seat should be holdable after cancel: %v", err)
	}
}

func TestGetAvailability(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	customer := uuid.New()

	h, _ := f.svc.HoldSeats(t.Context(), customer, f.catalog.eventID, f.seats[:1])
	if _, _, err := f.svc.CreateBooking(t.Context(), customer, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.HoldSeats(t.Context(), customer, f.catalog.eventID, f.seats[1:3]); err != nil {
		t.Fatal(err)
	}

	av, err := f.svc.GetAvailability(t.Context(), f.catalog.eventID)
	if err != nil {
		t.Fatal(err)
	}
	if len(av.Booked) != 1 || len(av.Held) != 2 {
		t.Errorf("availability = %+v, want 1 booked and 2 held", av)
	}
}

func wantIs(target error) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("error = %v, want %v", err, target)
		}
	}
}

func wantValidation(t *testing.T, err error) {
	t.Helper()
	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
}

func wantUnavailable(t *testing.T, err error) {
	t.Helper()
	var uerr *domain.SeatsUnavailableError
	if !errors.As(err, &uerr) {
		t.Fatalf("error = %v, want *SeatsUnavailableError", err)
	}
}
