// Package config loads the payment service configuration from environment variables.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the full service configuration.
type Config struct {
	HTTPAddr          string        `env:"HTTP_ADDR"         envDefault:":8080"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT"  envDefault:"15s"`
	LogLevel          string        `env:"LOG_LEVEL"         envDefault:"info"`
	LogFormat         string        `env:"LOG_FORMAT"        envDefault:"json"`
	TelemetryDisabled bool          `env:"OTEL_SDK_DISABLED" envDefault:"false"`

	KafkaBrokers string `env:"KAFKA_BROKERS" envDefault:"localhost:19092"`

	// Mock payment provider behaviour.
	ProviderLatency     time.Duration `env:"PROVIDER_LATENCY"      envDefault:"200ms"`
	ProviderFailureRate float64       `env:"PROVIDER_FAILURE_RATE" envDefault:"0.1"`

	DB DB `envPrefix:"DB_"`
}

// DB holds the Postgres connection settings.
type DB struct {
	URL      string `env:"URL,required"`
	MaxConns int32  `env:"MAX_CONNS" envDefault:"10"`
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}
