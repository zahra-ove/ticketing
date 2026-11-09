// Package config loads the booking service configuration from environment variables.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the full service configuration.
type Config struct {
	HTTPAddr        string        `env:"HTTP_ADDR"        envDefault:":8080"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
	LogLevel        string        `env:"LOG_LEVEL"        envDefault:"info"`
	LogFormat       string        `env:"LOG_FORMAT"       envDefault:"json"`
	// TelemetryDisabled uses the standard OpenTelemetry variable.
	TelemetryDisabled bool `env:"OTEL_SDK_DISABLED" envDefault:"false"`

	// HoldTTL is how long seats stay held before they are released automatically.
	HoldTTL time.Duration `env:"HOLD_TTL" envDefault:"10m"`
	// EventGRPCAddr is the address of the event service's internal gRPC API.
	EventGRPCAddr string `env:"EVENT_GRPC_ADDR" envDefault:"localhost:9000"`

	DB    DB    `envPrefix:"DB_"`
	Redis Redis `envPrefix:"REDIS_"`
}

// DB holds the Postgres connection settings.
type DB struct {
	URL             string        `env:"URL,required"`
	MaxConns        int32         `env:"MAX_CONNS"          envDefault:"10"`
	MinConns        int32         `env:"MIN_CONNS"          envDefault:"2"`
	MaxConnLifetime time.Duration `env:"MAX_CONN_LIFETIME"  envDefault:"30m"`
	MaxConnIdleTime time.Duration `env:"MAX_CONN_IDLE_TIME" envDefault:"5m"`
}

// Redis holds the Redis connection settings.
type Redis struct {
	Addr     string `env:"ADDR"     envDefault:"localhost:6379"`
	Password string `env:"PASSWORD"`
	DB       int    `env:"DB"       envDefault:"0"`
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}
