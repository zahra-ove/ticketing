// Command notification emails customers about their bookings. It consumes
// booking and payment events; its HTTP server only serves health probes.
//
// Usage:
//
//	notification serve        start the Kafka consumer and health endpoints (default)
//	notification migrate      apply database migrations and exit
//	notification healthcheck  probe /livez of a running server (for container health checks)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/zahra-ove/ticketing/pkg/bootstrap"
	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/pkg/kafkax"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/pkg/postgres"
	pgadapter "github.com/zahra-ove/ticketing/services/notification/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/notification/internal/adapter/smtp"
	"github.com/zahra-ove/ticketing/services/notification/internal/app"
	"github.com/zahra-ove/ticketing/services/notification/internal/config"
	"github.com/zahra-ove/ticketing/services/notification/internal/transport/consumer"
)

const serviceName = "notification"

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

	pool, err := postgres.NewPool(ctx, postgres.Config{URL: cfg.DB.URL, MaxConns: cfg.DB.MaxConns})
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
	inbox := pgadapter.NewInbox(pool)
	svc := app.NewService(smtp.NewMailer(smtp.Config{Addr: cfg.SMTPAddr, From: cfg.MailFrom}), inbox, app.ExampleDirectory)

	events, err := kafkax.NewConsumer(kafkax.ConsumerConfig{
		Brokers: cfg.KafkaBrokers,
		Group:   "notification",
		Topics:  []string{messaging.TopicBookingEvents, messaging.TopicPaymentEvents},
	}, consumer.NewHandler(svc), log)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", httpx.Livez)
	mux.HandleFunc("GET /readyz", httpx.Readyz(log, map[string]httpx.Pinger{"database": pool}))
	srv := httpx.NewServer(cfg.HTTPAddr, httpx.Middleware(mux, log), log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Run(gctx, srv, cfg.ShutdownTimeout, log) })
	g.Go(func() error { return events.Run(gctx) })
	g.Go(func() error { return cleanInbox(gctx, inbox, cfg.InboxRetentionDays, log) })
	return g.Wait()
}

// cleanInbox drops old message ids once an hour.
func cleanInbox(ctx context.Context, inbox *pgadapter.Inbox, days int, log *slog.Logger) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if n, err := inbox.DeleteOlderThan(ctx, days); err != nil {
			log.WarnContext(ctx, "inbox cleanup failed", slog.Any("error", err))
		} else if n > 0 {
			log.InfoContext(ctx, "inbox cleanup", slog.Int64("deleted", n))
		}
	}
}
