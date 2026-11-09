package app_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// memRepo is an in-memory Repository used to test the use cases in isolation.
type memRepo struct {
	mu     sync.Mutex
	events map[uuid.UUID]domain.Event
	seats  map[uuid.UUID][]domain.Seat
}

func newMemRepo() *memRepo {
	return &memRepo{events: map[uuid.UUID]domain.Event{}, seats: map[uuid.UUID][]domain.Seat{}}
}

func (r *memRepo) Create(_ context.Context, ev *domain.Event, seats []domain.Seat) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[ev.ID] = *ev
	r.seats[ev.ID] = seats
	return nil
}

func (r *memRepo) Get(_ context.Context, id uuid.UUID) (*domain.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.events[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &ev, nil
}

func (r *memRepo) ListPublished(_ context.Context, after *app.Cursor, limit int) ([]domain.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Event
	for _, ev := range r.events {
		if ev.Status != domain.StatusPublished {
			continue
		}
		if after != nil && !ev.StartsAt.After(after.StartsAt) &&
			(!ev.StartsAt.Equal(after.StartsAt) || ev.ID.String() <= after.ID.String()) {
			continue
		}
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b domain.Event) int {
		if c := a.StartsAt.Compare(b.StartsAt); c != 0 {
			return c
		}
		return slices.Compare(a.ID[:], b.ID[:])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *memRepo) Update(_ context.Context, id uuid.UUID, fn func(*domain.Event) error) (*domain.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.events[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if err := fn(&ev); err != nil {
		return nil, err
	}
	r.events[id] = ev
	return &ev, nil
}

func (r *memRepo) ListSeats(_ context.Context, eventID uuid.UUID) ([]domain.Seat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seats[eventID], nil
}

func (r *memRepo) GetSeats(_ context.Context, eventID uuid.UUID, ids []uuid.UUID) ([]domain.Seat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Seat
	for _, s := range r.seats[eventID] {
		if slices.Contains(ids, s.ID) {
			out = append(out, s)
		}
	}
	return out, nil
}

func TestService_GetSeats(t *testing.T) {
	t.Parallel()
	svc := newService(newMemRepo())

	ev, err := svc.CreateEvent(t.Context(), params(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	all, err := svc.ListSeats(t.Context(), ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := uuid.New()

	res, err := svc.GetSeats(t.Context(), ev.ID, []uuid.UUID{all[0].ID, unknown, all[2].ID})
	if err != nil {
		t.Fatalf("GetSeats() error = %v", err)
	}
	if res.Event.ID != ev.ID || len(res.Seats) != 2 {
		t.Errorf("got event %s with %d seats, want %s with 2", res.Event.ID, len(res.Seats), ev.ID)
	}
	if len(res.Missing) != 1 || res.Missing[0] != unknown {
		t.Errorf("missing = %v, want [%s]", res.Missing, unknown)
	}

	var verr *domain.ValidationError
	if _, err := svc.GetSeats(t.Context(), ev.ID, nil); !errors.As(err, &verr) {
		t.Errorf("empty ids error = %v, want *ValidationError", err)
	}
	if _, err := svc.GetSeats(t.Context(), uuid.New(), []uuid.UUID{all[0].ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown event error = %v, want ErrNotFound", err)
	}
}

func newService(repo app.Repository) *app.Service {
	return app.NewService(repo, app.WithClock(func() time.Time { return now }))
}

func params(startsIn time.Duration) domain.NewEventParams {
	return domain.NewEventParams{
		Title:    "Go Meetup",
		Venue:    "Hall 1",
		StartsAt: now.Add(startsIn),
		Sections: []domain.Section{{Name: "A", Rows: 2, SeatsPerRow: 3, PriceCents: 1000}},
	}
}

func TestService_CreateEvent_StoresSeats(t *testing.T) {
	t.Parallel()
	repo := newMemRepo()
	svc := newService(repo)

	ev, err := svc.CreateEvent(t.Context(), params(time.Hour))
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}

	seats, err := svc.ListSeats(t.Context(), ev.ID)
	if err != nil {
		t.Fatalf("ListSeats() error = %v", err)
	}
	if len(seats) != 6 {
		t.Errorf("got %d seats, want 6", len(seats))
	}
}

func TestService_ListSeats_UnknownEvent(t *testing.T) {
	t.Parallel()
	svc := newService(newMemRepo())

	_, err := svc.ListSeats(t.Context(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestService_PublishEvent(t *testing.T) {
	t.Parallel()
	svc := newService(newMemRepo())

	ev, err := svc.CreateEvent(t.Context(), params(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.PublishEvent(t.Context(), ev.ID)
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	if got.Status != domain.StatusPublished {
		t.Errorf("status = %q, want published", got.Status)
	}

	if _, err := svc.PublishEvent(t.Context(), ev.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("second publish error = %v, want ErrInvalidTransition", err)
	}
	if _, err := svc.PublishEvent(t.Context(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown event error = %v, want ErrNotFound", err)
	}
}

func TestService_ListPublishedEvents_Paginates(t *testing.T) {
	t.Parallel()
	svc := newService(newMemRepo())

	const total = 5
	for i := range total {
		ev, err := svc.CreateEvent(t.Context(), params(time.Duration(i+1)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.PublishEvent(t.Context(), ev.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A draft event must never be listed.
	if _, err := svc.CreateEvent(t.Context(), params(time.Minute)); err != nil {
		t.Fatal(err)
	}

	var (
		seen   []domain.Event
		cursor string
		pages  int
	)
	for {
		page, err := svc.ListPublishedEvents(t.Context(), cursor, 2)
		if err != nil {
			t.Fatalf("ListPublishedEvents() error = %v", err)
		}
		pages++
		seen = append(seen, page.Events...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("got %d events, want %d", len(seen), total)
	}
	if pages != 3 {
		t.Errorf("got %d pages, want 3", pages)
	}
	if !slices.IsSortedFunc(seen, func(a, b domain.Event) int { return a.StartsAt.Compare(b.StartsAt) }) {
		t.Error("events are not sorted by start time")
	}
}

func TestService_ListPublishedEvents_InvalidCursor(t *testing.T) {
	t.Parallel()
	svc := newService(newMemRepo())

	_, err := svc.ListPublishedEvents(t.Context(), "not-a-cursor", 10)

	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
}

func TestCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	want := app.Cursor{StartsAt: now, ID: uuid.New()}

	got, err := app.DecodeCursor(want.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartsAt.Equal(want.StartsAt) || got.ID != want.ID {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
