// Command auth issues access tokens and verifies them for the gateway.
//
// Usage:
//
//	auth serve        start the HTTP server (default)
//	auth healthcheck  probe /livez of a running server (for container health checks)
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/zahra-ove/ticketing/pkg/bootstrap"
	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/auth/internal/httpapi"
	"github.com/zahra-ove/ticketing/services/auth/internal/token"
	"github.com/zahra-ove/ticketing/services/auth/internal/users"
)

const serviceName = "auth"

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	HTTPAddr          string        `env:"HTTP_ADDR"         envDefault:":8080"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT"  envDefault:"15s"`
	LogLevel          string        `env:"LOG_LEVEL"         envDefault:"info"`
	LogFormat         string        `env:"LOG_FORMAT"        envDefault:"json"`
	TelemetryDisabled bool          `env:"OTEL_SDK_DISABLED" envDefault:"false"`

	// SigningKey is the base64-encoded 32-byte Ed25519 seed. When empty, a
	// random key is generated: fine for a demo, but tokens die on restart.
	SigningKey string        `env:"AUTH_SIGNING_KEY"`
	Issuer     string        `env:"AUTH_ISSUER"   envDefault:"ticketing-auth"`
	Audience   string        `env:"AUTH_AUDIENCE" envDefault:"ticketing"`
	TokenTTL   time.Duration `env:"TOKEN_TTL"     envDefault:"15m"`

	AdminPassword string `env:"ADMIN_PASSWORD,required"`
	DemoPassword  string `env:"DEMO_PASSWORD,required"`
	DemoSignup    bool   `env:"DEMO_SIGNUP" envDefault:"false"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := env.ParseAs[config]()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		return httpx.Healthcheck(ctx, cfg.HTTPAddr)
	}

	log, flush, err := bootstrap.Observability(ctx, bootstrap.Options{
		Service: serviceName, Version: version, LogLevel: cfg.LogLevel, LogFormat: cfg.LogFormat,
		TelemetryDisabled: cfg.TelemetryDisabled,
	})
	if err != nil {
		return err
	}
	defer flush()

	seed, err := signingSeed(cfg.SigningKey)
	if err != nil {
		return err
	}
	if cfg.SigningKey == "" {
		log.WarnContext(ctx, "AUTH_SIGNING_KEY is not set; using a random key, tokens will not survive a restart")
	}
	if cfg.DemoSignup {
		log.WarnContext(ctx, "DEMO_SIGNUP is on: any customer-* username can log in with the demo password")
	}

	issuer, err := token.NewIssuer(token.Config{Seed: seed, Issuer: cfg.Issuer, Audience: cfg.Audience, TTL: cfg.TokenTTL})
	if err != nil {
		return err
	}
	dir := users.NewDirectory(users.Config{AdminPassword: cfg.AdminPassword, DemoPassword: cfg.DemoPassword, DemoSignup: cfg.DemoSignup})

	srv := httpx.NewServer(cfg.HTTPAddr, httpapi.NewHandler(issuer, dir, log), log)
	log.InfoContext(ctx, "auth service ready", slog.String("issuer", cfg.Issuer), slog.Duration("token_ttl", cfg.TokenTTL))
	return httpx.Run(ctx, srv, cfg.ShutdownTimeout, log)
}

func signingSeed(b64 string) ([]byte, error) {
	if b64 == "" {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, fmt.Errorf("generate signing key: %w", err)
		}
		return seed, nil
	}
	seed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode AUTH_SIGNING_KEY: %w", err)
	}
	return seed, nil
}
