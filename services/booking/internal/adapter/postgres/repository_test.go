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
	"github.com/zahra-ove/ticketing/services/booking/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/domain"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17.6-alpine",
		tcpostgres.WithDatabase("booking"),
		tcpostgres.WithUsername("booking"),
		tcpostgres.WithPassword("booking"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(ctr) }()

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
		return 1
	}
	pool, err = pkgpostgres.NewPool(ctx, pkgpostgres.Config{URL: dsn, MaxConns: 30})
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

func newBooking(t *testing.T, eventID uuid.UUID, seats ...uuid.UUID) *domain.Booking {
	t.Helper()
	h := &domain.Hold{ID: uuid.New(), EventID: eventID, CustomerID: uuid.New()}
	for i, id := range seats {
		h.Seats = append(h.Seats, domain.Seat{ID: id, Label: fmt.Sprintf("A-1-%d", i+1), PriceCents: 1000})
	}
	b, err := domain.NewBooking(h, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRepository_CreateAndGet(t *testing.T) {
	repo := postgres.NewRepository(pool)
	b := newBooking(t, uuid.New(), uuid.New(), uuid.New())

	if err := repo.Create(t.Context(), b); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := repo.Get(t.Context(), b.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.HoldID != b.HoldID || got.TotalCents != 2000 || len(got.Seats) != 2 || got.Status != domain.StatusPendingPayment {
		t.Errorf("Get() = %+v", got)
	}
	if byHold, err := repo.GetByHoldID(t.Context(), b.HoldID); err != nil || byHold.ID != b.ID {
		t.Errorf("GetByHoldID() = %v, %v", byHold, err)
	}
	if _, err := repo.Get(t.Context(), uuid.New()); !errors.Is(err, domain.ErrBookingNotFound) {
		t.Errorf("Get(unknown) error = %v", err)
	}
}

func TestRepository_SameHoldTwice(t *testing.T) {
	repo := postgres.NewRepository(pool)
	b := newBooking(t, uuid.New(), uuid.New())
	if err := repo.Create(t.Context(), b); err != nil {
		t.Fatal(err)
	}

	again := newBooking(t, b.EventID, uuid.New())
	again.HoldID = b.HoldID

	if err := repo.Create(t.Context(), again); !errors.Is(err, app.ErrHoldAlreadyUsed) {
		t.Fatalf("error = %v, want ErrHoldAlreadyUsed", err)
	}
}

func TestRepository_CancelFreesSeats(t *testing.T) {
	repo := postgres.NewRepository(pool)
	eventID, seat := uuid.New(), uuid.New()
	first := newBooking(t, eventID, seat)
	if err := repo.Create(t.Context(), first); err != nil {
		t.Fatal(err)
	}

	var unavailable *domain.SeatsUnavailableError
	if err := repo.Create(t.Context(), newBooking(t, eventID, seat)); !errors.As(err, &unavailable) || unavailable.SeatIDs[0] != seat {
		t.Fatalf("second booking of the same seat: error = %v, want SeatsUnavailableError", err)
	}

	if _, err := repo.Update(t.Context(), first.ID, func(b *domain.Booking) error { return b.Cancel(time.Now(), domain.CancelByCustomer) }); err != nil {
		t.Fatal(err)
	}
	if booked, _ := repo.BookedSeats(t.Context(), eventID, nil); len(booked) != 0 {
		t.Errorf("cancelled booking still blocks seats: %v", booked)
	}
	if err := repo.Create(t.Context(), newBooking(t, eventID, seat)); err != nil {
		t.Errorf("seat should be bookable after cancel: %v", err)
	}
}

// Even if Redis and the application checks were bypassed, the database
// accepts exactly one of many concurrent bookings for the same seat.
func TestRepository_ConcurrentBookingsOfSameSeat(t *testing.T) {
	repo := postgres.NewRepository(pool)
	eventID, seat := uuid.New(), uuid.New()

	const attempts = 20
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := repo.Create(context.Background(), newBooking(t, eventID, seat))
			var unavailable *domain.SeatsUnavailableError
			switch {
			case err == nil:
				mu.Lock()
				successes++
				mu.Unlock()
			case !errors.As(err, &unavailable):
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("got %d bookings for one seat, want exactly 1", successes)
	}
}

// Every state change writes its event to the outbox in the same transaction.
func TestRepository_WritesEventsToOutbox(t *testing.T) {
	repo := postgres.NewRepository(pool)
	b := newBooking(t, uuid.New(), uuid.New())
	if err := repo.Create(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(t.Context(), b.ID, func(b *domain.Booking) error {
		return b.Cancel(time.Now(), domain.CancelByCustomer)
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(t.Context(), `SELECT type FROM outbox WHERE key = $1 ORDER BY created_at, id`, b.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
	}
	want := []string{"ticketing.booking.v1.BookingCreated", "ticketing.booking.v1.BookingCancelled"}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Errorf("outbox types = %v, want %v", types, want)
	}

	got, err := repo.Get(t.Context(), b.ID)
	if err != nil || got.CancelReason != domain.CancelByCustomer {
		t.Errorf("cancel reason = %q (%v), want customer", got.CancelReason, err)
	}
}

// A failed transaction must not leave its event behind.
func TestRepository_RolledBackChangeHasNoEvent(t *testing.T) {
	repo := postgres.NewRepository(pool)
	eventID, seat := uuid.New(), uuid.New()
	if err := repo.Create(t.Context(), newBooking(t, eventID, seat)); err != nil {
		t.Fatal(err)
	}
	loser := newBooking(t, eventID, seat) // same seat: the insert fails
	if err := repo.Create(t.Context(), loser); err == nil {
		t.Fatal("expected the second booking of the seat to fail")
	}

	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE key = $1`, loser.ID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("found %d outbox rows for a booking that was never stored", n)
	}
}
