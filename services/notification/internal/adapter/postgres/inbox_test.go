//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pkgpostgres "github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/services/notification/internal/adapter/postgres"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17.6-alpine",
		tcpostgres.WithDatabase("notification"), tcpostgres.WithUsername("notification"), tcpostgres.WithPassword("notification"),
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
	if pool, err = pkgpostgres.NewPool(ctx, pkgpostgres.Config{URL: dsn, MaxConns: 20}); err != nil {
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

// Ten concurrent deliveries of one message run the side effect exactly once.
func TestInbox_ConcurrentDuplicatesRunOnce(t *testing.T) {
	inbox := postgres.NewInbox(pool)
	msg := uuid.New()

	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := inbox.Once(context.Background(), msg, func(context.Context) error {
				calls.Add(1)
				return nil
			})
			if err != nil {
				t.Errorf("Once() error = %v", err)
			}
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("side effect ran %d times, want 1", got)
	}
}

// A failed side effect does not mark the message as processed.
func TestInbox_FailureAllowsRetry(t *testing.T) {
	inbox := postgres.NewInbox(pool)
	msg := uuid.New()

	if err := inbox.Once(t.Context(), msg, func(context.Context) error { return errors.New("smtp down") }); err == nil {
		t.Fatal("expected the error to be returned")
	}

	ran := false
	if err := inbox.Once(t.Context(), msg, func(context.Context) error { ran = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("message must be processed on retry after a failure")
	}
}
