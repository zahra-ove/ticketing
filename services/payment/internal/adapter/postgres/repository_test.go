//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pkgpostgres "github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/services/payment/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/payment/internal/app"
	"github.com/zahra-ove/ticketing/services/payment/internal/domain"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17.6-alpine",
		tcpostgres.WithDatabase("payment"), tcpostgres.WithUsername("payment"), tcpostgres.WithPassword("payment"),
		tcpostgres.BasicWaitStrategies())
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
	if pool, err = pkgpostgres.NewPool(ctx, pkgpostgres.Config{URL: dsn}); err != nil {
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

func outboxTypes(t *testing.T, bookingID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT type FROM outbox WHERE key = $1 ORDER BY created_at, id`, bookingID.String())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		types = append(types, s)
	}
	return types
}

func TestRepository_CreateRefundAndEvents(t *testing.T) {
	repo := postgres.NewRepository(pool)
	c := domain.Charge{BookingID: uuid.New(), CustomerID: uuid.New(), AmountCents: 4200}
	p, err := domain.NewSucceeded(c, "ch_abc", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(t.Context(), p); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	again, _ := domain.NewFailed(c, "card_declined", time.Now())
	if err := repo.Create(t.Context(), again); !errors.Is(err, app.ErrDuplicatePayment) {
		t.Fatalf("second payment for the booking: error = %v, want ErrDuplicatePayment", err)
	}

	if _, err := repo.Update(t.Context(), p.ID, func(p *domain.Payment) error { return p.Refund(time.Now()) }); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByBookingID(t.Context(), c.BookingID)
	if err != nil || got.Status != domain.StatusRefunded || got.ProviderRef != "ch_abc" {
		t.Fatalf("GetByBookingID() = %+v, %v", got, err)
	}

	want := fmt.Sprint([]string{"ticketing.payment.v1.PaymentSucceeded", "ticketing.payment.v1.PaymentRefunded"})
	if types := fmt.Sprint(outboxTypes(t, c.BookingID)); types != want {
		t.Errorf("outbox = %s, want %s", types, want)
	}
}

func TestRepository_GetByBookingID_NotFound(t *testing.T) {
	repo := postgres.NewRepository(pool)
	if _, err := repo.GetByBookingID(t.Context(), uuid.New()); !errors.Is(err, domain.ErrPaymentNotFound) {
		t.Fatalf("error = %v, want ErrPaymentNotFound", err)
	}
}
