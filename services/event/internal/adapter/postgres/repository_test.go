//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pkgpostgres "github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/services/event/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/domain"
)

// pool is shared by every test in the package; one container is much faster
// than one per test. Tests stay independent because each creates its own events.
var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:17.6-alpine",
		tcpostgres.WithDatabase("event"),
		tcpostgres.WithUsername("event"),
		tcpostgres.WithPassword("event"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintf(os.Stderr, "terminate container: %v\n", err)
		}
	}()

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
		return 1
	}

	pool, err = pkgpostgres.NewPool(ctx, pkgpostgres.Config{URL: dsn})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		return 1
	}
	defer pool.Close()

	if _, err := postgres.Migrate(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		return 1
	}

	return m.Run()
}

func newEvent(t *testing.T, startsIn time.Duration) *domain.Event {
	t.Helper()
	ev, err := domain.NewEvent(domain.NewEventParams{
		Title:    "Integration test event",
		Venue:    "Test Hall",
		StartsAt: time.Now().Add(startsIn).Truncate(time.Microsecond),
		Sections: []domain.Section{
			{Name: "A", Rows: 2, SeatsPerRow: 3, PriceCents: 1500},
			{Name: "B", Rows: 1, SeatsPerRow: 4, PriceCents: 900},
		},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func create(t *testing.T, repo *postgres.Repository, ev *domain.Event) {
	t.Helper()
	seats, err := ev.GenerateSeats()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(t.Context(), ev, seats); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestRepository_CreateAndGet(t *testing.T) {
	repo := postgres.NewRepository(pool)
	ev := newEvent(t, 24*time.Hour)
	create(t, repo, ev)

	got, err := repo.Get(t.Context(), ev.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Title != ev.Title || !got.StartsAt.Equal(ev.StartsAt) || got.Status != domain.StatusDraft {
		t.Errorf("Get() = %+v, want %+v", got, ev)
	}
	if len(got.Sections) != 2 || got.Sections[1].PriceCents != 900 {
		t.Errorf("sections = %+v", got.Sections)
	}

	seats, err := repo.ListSeats(t.Context(), ev.ID)
	if err != nil {
		t.Fatalf("ListSeats() error = %v", err)
	}
	if len(seats) != ev.Capacity() {
		t.Fatalf("got %d seats, want %d", len(seats), ev.Capacity())
	}
	// Seats come back in venue order, not alphabetical section order.
	if first, last := seats[0].Label(), seats[len(seats)-1].Label(); first != "A-1-1" || last != "B-1-4" {
		t.Errorf("seat order: first = %s, last = %s, want A-1-1 and B-1-4", first, last)
	}
}

func TestRepository_Get_NotFound(t *testing.T) {
	repo := postgres.NewRepository(pool)

	_, err := repo.Get(t.Context(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestRepository_ListPublished_KeysetPagination(t *testing.T) {
	repo := postgres.NewRepository(pool)

	// Far in the future so that events from other tests sort before these.
	base := 1000 * 24 * time.Hour
	var want []uuid.UUID
	for i := range 3 {
		ev := newEvent(t, base+time.Duration(i)*time.Hour)
		create(t, repo, ev)
		if _, err := repo.Update(t.Context(), ev.ID, func(e *domain.Event) error { return e.Publish(time.Now()) }); err != nil {
			t.Fatal(err)
		}
		want = append(want, ev.ID)
	}

	first, err := repo.ListPublished(t.Context(), &app.Cursor{StartsAt: time.Now().Add(base - time.Hour)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != want[0] || first[1].ID != want[1] {
		t.Fatalf("first page = %v, want %v", ids(first), want[:2])
	}

	last := first[1]
	second, err := repo.ListPublished(t.Context(), &app.Cursor{StartsAt: last.StartsAt, ID: last.ID}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != want[2] {
		t.Fatalf("second page = %v, want %v", ids(second), want[2:])
	}
}

// Two concurrent publishes of the same event: the row lock guarantees that
// exactly one succeeds and the other sees the already-published state.
func TestRepository_Update_IsSerialized(t *testing.T) {
	repo := postgres.NewRepository(pool)
	ev := newEvent(t, 24*time.Hour)
	create(t, repo, ev)

	const workers = 10
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Update(t.Context(), ev.ID, func(e *domain.Event) error { return e.Publish(time.Now()) })
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
				return
			}
			if !errors.Is(err, domain.ErrInvalidTransition) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("got %d successful publishes, want exactly 1", successes)
	}
}

func ids(events []domain.Event) []uuid.UUID {
	out := make([]uuid.UUID, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}
