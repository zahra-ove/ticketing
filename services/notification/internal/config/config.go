// Package config loads the notification service configuration from environment variables.
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

	SMTPAddr string `env:"SMTP_ADDR" envDefault:"localhost:1025"`
	MailFrom string `env:"MAIL_FROM" envDefault:"Ticketing <no-reply@ticketing.local>"`

	// InboxRetentionDays is how long processed message ids are kept.
	InboxRetentionDays int `env:"INBOX_RETENTION_DAYS" envDefault:"7"`

	DB DB `envPrefix:"DB_"`
}

// DB holds the Postgres connection settings.
type DB struct {
	URL      string `env:"URL,required"`
	MaxConns int32  `env:"MAX_CONNS" envDefault:"5"`
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}
