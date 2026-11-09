// Command payment runs the payment service. It charges customers for new
// bookings and refunds them when the booking saga compensates.
//
// Usage:
//
//	payment serve        start the HTTP API, the Kafka consumer and the outbox relay (default)
//	payment migrate      apply database migrations and exit
//	payment healthcheck  probe /livez of a running server (for container health checks)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/zahra-ove/ticketing/pkg/bootstrap"
	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/pkg/kafkax"
	"github.com/zahra-ove/ticketing/pkg/messaging"
	"github.com/zahra-ove/ticketing/pkg/outbox"
	"github.com/zahra-ove/ticketing/pkg/postgres"
	pgadapter "github.com/zahra-ove/ticketing/services/payment/internal/adapter/postgres"
	"github.com/zahra-ove/ticketing/services/payment/internal/adapter/provider"
	"github.com/zahra-ove/ticketing/services/payment/internal/app"
	"github.com/zahra-ove/ticketing/services/payment/internal/config"
	"github.com/zahra-ove/ticketing/services/payment/internal/transport/consumer"
	"github.com/zahra-ove/ticketing/services/payment/internal/transport/httpapi"
)

const serviceName = "payment"

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
	svc, err := app.NewInstrumentedService(app.NewService(
		provider.NewMock(provider.Config{Latency: cfg.ProviderLatency, FailureRate: cfg.ProviderFailureRate}),
		pgadapter.NewRepository(pool),
	))
	if err != nil {
		return err
	}

	producer, err := kafkax.NewProducer(cfg.KafkaBrokers, serviceName+"-outbox")
	if err != nil {
		return err
	}
	defer producer.Close()
	relay, err := outbox.NewRelay(pool, producer, outbox.RelayConfig{}, log)
	if err != nil {
		return err
	}

	bookingEvents, err := kafkax.NewConsumer(kafkax.ConsumerConfig{
		Brokers: cfg.KafkaBrokers,
		Group:   "payment.booking-events",
		Topics:  []string{messaging.TopicBookingEvents},
	}, consumer.NewHandler(svc), log)
	if err != nil {
		return err
	}

	srv := httpx.NewServer(cfg.HTTPAddr, httpapi.NewHandler(svc, map[string]httpx.Pinger{"database": pool}, log), log)

	// Everything stops together: if one part fails, the context is cancelled
	// for the others and the process exits so the orchestrator restarts it.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Run(gctx, srv, cfg.ShutdownTimeout, log) })
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return bookingEvents.Run(gctx) })
	return g.Wait()
}
