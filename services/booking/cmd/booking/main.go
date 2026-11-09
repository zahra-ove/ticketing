// Command booking runs the booking service: seat holds and bookings.
//
// Usage:
//
//	booking serve        start the HTTP server (default)
//	booking migrate      apply database migrations and exit
//	booking healthcheck  probe /livez of a running server (for container health checks)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zahra-ove/ticketing/pkg/bootstrap"
	"github.com/zahra-ove/ticketing/pkg/grpcx"
	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/pkg/redisx"
	"github.com/zahra-ove/ticketing/services/booking/internal/adapter/catalog"
	pgadapter "github.com/zahra-ove/ticketing/services/booking/internal/adapter/postgres"
	redisadapter "github.com/zahra-ove/ticketing/services/booking/internal/adapter/redis"
	"github.com/zahra-ove/ticketing/services/booking/internal/app"
	"github.com/zahra-ove/ticketing/services/booking/internal/config"
	"github.com/zahra-ove/ticketing/services/booking/internal/transport/httpapi"
)

const serviceName = "booking"

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}

func run() error {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if cmd == "healthcheck" {
		return httpx.Healthcheck(ctx, cfg.HTTPAddr)
	}

	log, flush, err := bootstrap.Observability(ctx, bootstrap.Options{
		Service:           serviceName,
		Version:           version,
		LogLevel:          cfg.LogLevel,
		LogFormat:         cfg.LogFormat,
		TelemetryDisabled: cfg.TelemetryDisabled || cmd == "migrate",
	})
	if err != nil {
		return err
	}
	defer flush()

	pool, err := postgres.NewPool(ctx, postgres.Config{
		URL:             cfg.DB.URL,
		MaxConns:        cfg.DB.MaxConns,
		MinConns:        cfg.DB.MinConns,
		MaxConnLifetime: cfg.DB.MaxConnLifetime,
		MaxConnIdleTime: cfg.DB.MaxConnIdleTime,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "migrate":
		n, err := pgadapter.Migrate(ctx, pool)
		if err != nil {
			return err
		}
		log.InfoContext(ctx, "migrations applied", slog.Int("count", n))
		return nil
	case "serve":
		return serve(ctx, cfg, pool, log)
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate or healthcheck)", cmd)
	}
}

func serve(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) error {
	rdb, err := redisx.NewClient(ctx, redisx.Config{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	// grpc.NewClient connects lazily, so the booking service starts even if the
	// event service is not up yet; calls to it fail fast and are retried.
	eventConn, err := grpcx.NewClient(cfg.EventGRPCAddr)
	if err != nil {
		return err
	}
	defer func() { _ = eventConn.Close() }()

	svc, err := app.NewInstrumentedService(app.NewService(
		catalog.NewClient(eventConn),
		redisadapter.NewHoldStore(rdb),
		pgadapter.NewRepository(pool),
		app.WithHoldTTL(cfg.HoldTTL),
		app.WithLogger(log),
	))
	if err != nil {
		return err
	}

	// Readiness covers only this service's own stores. The event service is
	// left out on purpose: its outage must not take booking out of rotation.
	deps := map[string]httpx.Pinger{
		"database": pool,
		"redis":    httpx.PingerFunc(func(ctx context.Context) error { return rdb.Ping(ctx).Err() }),
	}
	srv := httpx.NewServer(cfg.HTTPAddr, httpapi.NewHandler(svc, deps, log), log)
	return httpx.Run(ctx, srv, cfg.ShutdownTimeout, log)
}
