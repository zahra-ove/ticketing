//go:build integration

package redis_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/zahra-ove/ticketing/services/booking/internal/adapter/redis"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

var rdb *goredis.Client

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		fmt.Fprintf(os.Stderr, "start redis container: %v\n", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(ctr) }()

	uri, err := ctr.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
		return 1
	}
	opts, err := goredis.ParseURL(uri)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse url: %v\n", err)
		return 1
	}
	rdb = goredis.NewClient(opts)
	defer func() { _ = rdb.Close() }()

	return m.Run()
}

func newHold(eventID uuid.UUID, seats ...uuid.UUID) *domain.Hold {
	h := &domain.Hold{ID: uuid.New(), EventID: eventID, CustomerID: uuid.New(), ExpiresAt: time.Now().Add(time.Minute)}
	for i, id := range seats {
		h.Seats = append(h.Seats, domain.Seat{ID: id, Label: fmt.Sprintf("A-1-%d", i+1), PriceCents: 1000})
	}
	return h
}

func TestHoldStore_HoldGetRelease(t *testing.T) {
	store := redis.NewHoldStore(rdb)
	eventID, s1, s2 := uuid.New(), uuid.New(), uuid.New()
	h := newHold(eventID, s1, s2)

	if err := store.Hold(t.Context(), h, time.Minute); err != nil {
		t.Fatalf("Hold() error = %v", err)
	}

	got, err := store.Get(t.Context(), h.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.CustomerID != h.CustomerID || len(got.Seats) != 2 || got.TotalCents() != 2000 {
		t.Errorf("Get() = %+v", got)
	}
	if err := store.Verify(t.Context(), h); err != nil {
		t.Errorf("Verify() error = %v", err)
	}
	held, err := store.HeldSeats(t.Context(), eventID)
	if err != nil || len(held) != 2 {
		t.Errorf("HeldSeats() = %v, %v", held, err)
	}

	if err := store.Release(t.Context(), h); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := store.Get(t.Context(), h.ID); !errors.Is(err, domain.ErrHoldNotFound) {
		t.Errorf("Get() after release error = %v, want ErrHoldNotFound", err)
	}
	if held, _ := store.HeldSeats(t.Context(), eventID); len(held) != 0 {
		t.Errorf("seats still held after release: %v", held)
	}
}

func TestHoldStore_AllOrNothing(t *testing.T) {
	store := redis.NewHoldStore(rdb)
	eventID, s1, s2, s3 := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	if err := store.Hold(t.Context(), newHold(eventID, s1, s2), time.Minute); err != nil {
		t.Fatal(err)
	}

	// s2 is taken, so s3 must not be held either.
	err := store.Hold(t.Context(), newHold(eventID, s2, s3), time.Minute)
	var unavailable *domain.SeatsUnavailableError
	if !errors.As(err, &unavailable) || len(unavailable.SeatIDs) != 1 || unavailable.SeatIDs[0] != s2 {
		t.Fatalf("error = %v, want SeatsUnavailableError for %s", err, s2)
	}
	if err := store.Hold(t.Context(), newHold(eventID, s3), time.Minute); err != nil {
		t.Errorf("s3 should still be free: %v", err)
	}
}

func TestHoldStore_Expiry(t *testing.T) {
	store := redis.NewHoldStore(rdb)
	eventID, seat := uuid.New(), uuid.New()
	h := newHold(eventID, seat)

	if err := store.Hold(t.Context(), h, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	if err := store.Verify(t.Context(), h); !errors.Is(err, domain.ErrHoldNotFound) {
		t.Errorf("Verify() after expiry = %v, want ErrHoldNotFound", err)
	}
	if held, _ := store.HeldSeats(t.Context(), eventID); len(held) != 0 {
		t.Errorf("expired seats still listed: %v", held)
	}
	if err := store.Hold(t.Context(), newHold(eventID, seat), time.Minute); err != nil {
		t.Errorf("expired seat should be holdable again: %v", err)
	}
}

func TestHoldStore_ReleaseDoesNotFreeSomeoneElsesSeat(t *testing.T) {
	store := redis.NewHoldStore(rdb)
	eventID, seat := uuid.New(), uuid.New()
	first := newHold(eventID, seat)

	if err := store.Hold(t.Context(), first, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // first expires
	second := newHold(eventID, seat)
	if err := store.Hold(t.Context(), second, time.Minute); err != nil {
		t.Fatal(err)
	}

	// A late release of the expired hold must not unlock the new owner's seat.
	if err := store.Release(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(t.Context(), second); err != nil {
		t.Errorf("second hold lost its seat: %v", err)
	}
}

// 50 customers race for overlapping seats. Every seat must end up held by at
// most one customer, and every customer either gets all their seats or none.
func TestHoldStore_ConcurrentHolds(t *testing.T) {
	store := redis.NewHoldStore(rdb)
	eventID := uuid.New()
	seats := make([]uuid.UUID, 5)
	for i := range seats {
		seats[i] = uuid.New()
	}

	const customers = 50
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []*domain.Hold
	)
	for i := range customers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each customer wants two neighbouring seats, so requests overlap.
			h := newHold(eventID, seats[i%4], seats[i%4+1])
			err := store.Hold(context.Background(), h, time.Minute)
			var unavailable *domain.SeatsUnavailableError
			switch {
			case err == nil:
				mu.Lock()
				wins = append(wins, h)
				mu.Unlock()
			case !errors.As(err, &unavailable):
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	owner := map[uuid.UUID]uuid.UUID{}
	for _, h := range wins {
		for _, s := range h.Seats {
			if prev, ok := owner[s.ID]; ok {
				t.Fatalf("seat %s held by both %s and %s", s.ID, prev, h.ID)
			}
			owner[s.ID] = h.ID
		}
		if err := store.Verify(t.Context(), h); err != nil {
			t.Errorf("winning hold %s does not own its seats: %v", h.ID, err)
		}
	}
	if len(wins) == 0 || len(wins) > 2 {
		t.Errorf("got %d winning holds; with 5 seats and pairs, want 1 or 2", len(wins))
	}
}
