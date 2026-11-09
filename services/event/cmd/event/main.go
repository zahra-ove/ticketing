// Command event runs the event catalog service.
//
// Usage:
//
//	event serve        start the HTTP (public) and gRPC (internal) servers (default)
//	event migrate      apply database migrations and exit
//	event healthcheck  probe /livez of a running server (for container health checks)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	eventv1 "github.com/zahra-ove/ticketing/api/gen/ticketing/event/v1"
	"github.com/zahra-ove/ticketing/pkg/bootstrap"
	"github.com/zahra-ove/ticketing/pkg/grpcx"
	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/pkg/postgres"
	pgadapter "github.com/zahra-ove/ticketing/services/event/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/config"
	"github.com/zahra-ove/ticketing/services/event/internal/transport/grpcapi"
	"github.com/zahra-ove/ticketing/services/event/internal/transport/httpapi"
)

const serviceName = "event"

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

	// The context is cancelled on SIGINT/SIGTERM, which starts a graceful shutdown.
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
		Service:   serviceName,
		Version:   version,
		LogLevel:  cfg.LogLevel,
		LogFormat: cfg.LogFormat,
		// The short-lived migrate job has nothing worth exporting.
		TelemetryDisabled: cfg.TelemetryDisabled || cmd == "migrate",
	})
	if err != nil {
		return err
	}
	defer flush() // deferred first, so it runs last and exports what shutdown logged

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
		svc, err := app.NewInstrumentedService(app.NewService(pgadapter.NewRepository(pool)))
		if err != nil {
			return err
		}
		return serve(ctx, cfg, svc, pool, log)
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate or healthcheck)", cmd)
	}
}

// serve runs the HTTP and gRPC servers until ctx is cancelled. If either
// server fails, the other is shut down too.
func serve(ctx context.Context, cfg config.Config, svc *app.InstrumentedService, db httpx.Pinger, log *slog.Logger) error {
	httpSrv := httpx.NewServer(cfg.HTTPAddr, httpapi.NewHandler(svc, db, log), log)

	grpcSrv, health := grpcx.NewServer(log)
	eventv1.RegisterCatalogServiceServer(grpcSrv, grpcapi.NewServer(svc))

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Run(gctx, httpSrv, cfg.ShutdownTimeout, log) })
	g.Go(func() error { return grpcx.Run(gctx, grpcSrv, health, cfg.GRPCAddr, cfg.ShutdownTimeout, log) })
	return g.Wait()
}
