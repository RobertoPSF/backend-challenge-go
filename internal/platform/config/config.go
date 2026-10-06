package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

var Module = fx.Module("config", fx.Provide(Load))

type Config struct {
	HTTPAddr   string     `env:"HTTP_ADDR" envDefault:":8080"`
	LogLevel   slog.Level `env:"LOG_LEVEL" envDefault:"INFO"`
	InstanceID string     `env:"INSTANCE_ID"`
	Database   Database
	AWS        AWS
	OIDC       OIDC
	Pending    Pending
}

type Pending struct {
	BaseBackoff time.Duration `env:"PENDING_BASE_BACKOFF" envDefault:"1s"`
	MaxBackoff  time.Duration `env:"PENDING_MAX_BACKOFF" envDefault:"5m"`
	MaxAttempts int           `env:"PENDING_MAX_ATTEMPTS" envDefault:"10"`
	TTL         time.Duration `env:"PENDING_TTL" envDefault:"30m"`
}

type OIDC struct {
	Issuer   string `env:"OIDC_ISSUER,required,notEmpty"`
	JWKSURL  string `env:"OIDC_JWKS_URL,required,notEmpty"`
	Audience string `env:"OIDC_AUDIENCE" envDefault:"wagering-api"`
}

type Database struct {
	URL              string        `env:"DATABASE_URL,required,notEmpty"`
	MaxConns         int32         `env:"DB_MAX_CONNS" envDefault:"10"`
	LockTimeout      time.Duration `env:"DB_LOCK_TIMEOUT" envDefault:"5s"`
	StatementTimeout time.Duration `env:"DB_STATEMENT_TIMEOUT" envDefault:"10s"`
}

type AWS struct {
	Region      string `env:"AWS_REGION,required,notEmpty"`
	EndpointURL string `env:"AWS_ENDPOINT_URL"`
	InputQueue  string `env:"SQS_INPUT_QUEUE" envDefault:"wager-transactions.fifo"`
	EventsQueue string `env:"SQS_EVENTS_QUEUE" envDefault:"wallet-events.fifo"`
}

func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID, _ = os.Hostname()
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	if c.Database.MaxConns < 1 {
		errs = append(errs, errors.New("DB_MAX_CONNS must be >= 1"))
	}
	if c.Database.LockTimeout <= 0 || c.Database.StatementTimeout <= 0 {
		errs = append(errs, errors.New("DB_LOCK_TIMEOUT and DB_STATEMENT_TIMEOUT must be > 0"))
	}
	if c.Database.LockTimeout >= c.Database.StatementTimeout {
		errs = append(errs, errors.New("DB_LOCK_TIMEOUT must be lower than DB_STATEMENT_TIMEOUT"))
	}
	if c.Pending.BaseBackoff <= 0 || c.Pending.MaxBackoff < c.Pending.BaseBackoff || c.Pending.MaxAttempts < 1 || c.Pending.TTL <= 0 {
		errs = append(errs, errors.New("PENDING_* settings must be positive and PENDING_MAX_BACKOFF >= PENDING_BASE_BACKOFF"))
	}
	for name, queue := range map[string]string{"SQS_INPUT_QUEUE": c.AWS.InputQueue, "SQS_EVENTS_QUEUE": c.AWS.EventsQueue} {
		if !strings.HasSuffix(queue, ".fifo") {
			errs = append(errs, fmt.Errorf("%s must be a FIFO queue (.fifo): %q", name, queue))
		}
	}
	if c.InstanceID == "" {
		errs = append(errs, errors.New("INSTANCE_ID is empty and hostname is unavailable"))
	}
	return errors.Join(errs...)
}
