// Command event runs the event catalog service.
//
// Usage:
//
//	event serve        start the HTTP server (default)
//	event migrate      apply database migrations and exit
//	event healthcheck  probe /livez of a running server (for container health checks)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"

	"github.com/zahra-ove/ticketing/pkg/logger"
	"github.com/zahra-ove/ticketing/pkg/postgres"
	"github.com/zahra-ove/ticketing/pkg/telemetry"
	pgadapter "github.com/zahra-ove/ticketing/services/event/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/event/internal/app"
	"github.com/zahra-ove/ticketing/services/event/internal/config"
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
		return healthcheck(ctx, cfg.HTTPAddr)
	}

	// Telemetry first, so the logger, the pool and the HTTP server are instrumented.
	// The short-lived migrate job has nothing worth exporting.
	tel, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName:    serviceName,
		ServiceVersion: version,
		Disabled:       cfg.TelemetryDisabled || cmd == "migrate",
	})
	if err != nil {
		return err
	}
	// Deferred first, so it runs last: flushes spans and logs written during shutdown.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			fmt.Fprintf(os.Stderr, "%s: flush telemetry: %v\n", serviceName, err)
		}
	}()

	log, err := logger.New(os.Stdout, logger.Config{
		Level:   cfg.LogLevel,
		Format:  logger.Format(cfg.LogFormat),
		Service: serviceName,
		Version: version,
	}, otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(tel.LoggerProvider)))
	if err != nil {
		return err
	}
	// Report exporter failures (e.g. collector unreachable) through our logger.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.WarnContext(ctx, "opentelemetry error", slog.Any("error", err))
	}))

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
		return serve(ctx, cfg, httpapi.NewHandler(svc, pool, log), log)
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate or healthcheck)", cmd)
	}
}

// serve runs the HTTP server until ctx is cancelled, then drains in-flight
// requests for at most cfg.ShutdownTimeout.
func serve(ctx context.Context, cfg config.Config, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.InfoContext(gctx, "http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		<-gctx.Done()
		log.InfoContext(gctx, "shutting down http server")

		// gctx is already cancelled; derive a fresh deadline that keeps its values.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown http server: %w", err)
		}
		log.InfoContext(shutdownCtx, "http server stopped")
		return nil
	})

	return g.Wait()
}

// healthcheck lets a distroless container (no curl, no shell) check itself.
func healthcheck(ctx context.Context, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse HTTP_ADDR %q: %w", addr, err)
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/livez", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe /livez: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // nothing useful to do with a close error on a probe

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe /livez: unexpected status %d", resp.StatusCode)
	}
	return nil
}
