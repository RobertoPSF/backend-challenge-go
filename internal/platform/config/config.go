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
	Consumer   Consumer
	Outbox     Outbox
}

type Outbox struct {
	Enabled        bool          `env:"ENABLE_OUTBOX_PUBLISHER" envDefault:"true"`
	Workers        int           `env:"OUTBOX_WORKERS" envDefault:"1"`
	BatchSize      int           `env:"OUTBOX_BATCH_SIZE" envDefault:"50"`
	Lease          time.Duration `env:"OUTBOX_LEASE" envDefault:"30s"`
	PollInterval   time.Duration `env:"OUTBOX_POLL_INTERVAL" envDefault:"500ms"`
	PublishTimeout time.Duration `env:"OUTBOX_PUBLISH_TIMEOUT" envDefault:"10s"`
	RetryBaseDelay time.Duration `env:"OUTBOX_RETRY_BASE_DELAY" envDefault:"1s"`
	RetryMaxDelay  time.Duration `env:"OUTBOX_RETRY_MAX_DELAY" envDefault:"5m"`
}

type Consumer struct {
	Enabled           bool          `env:"ENABLE_CONSUMER" envDefault:"true"`
	Name              string        `env:"SQS_CONSUMER_NAME" envDefault:"wallet-service"`
	Workers           int           `env:"SQS_WORKERS" envDefault:"2"`
	WaitTime          time.Duration `env:"SQS_WAIT_TIME" envDefault:"10s"`
	VisibilityTimeout time.Duration `env:"SQS_VISIBILITY_TIMEOUT" envDefault:"30s"`
	HandlerTimeout    time.Duration `env:"SQS_HANDLER_TIMEOUT" envDefault:"20s"`
	RetryBaseDelay    time.Duration `env:"SQS_RETRY_BASE_DELAY" envDefault:"2s"`
	RetryMaxDelay     time.Duration `env:"SQS_RETRY_MAX_DELAY" envDefault:"5m"`
	KnownProviders    []string      `env:"KNOWN_PROVIDERS" envDefault:"provider-a,provider-b" envSeparator:","`
}

type Pending struct {
	BaseBackoff  time.Duration `env:"PENDING_BASE_BACKOFF" envDefault:"1s"`
	MaxBackoff   time.Duration `env:"PENDING_MAX_BACKOFF" envDefault:"5m"`
	MaxAttempts  int           `env:"PENDING_MAX_ATTEMPTS" envDefault:"10"`
	TTL          time.Duration `env:"PENDING_TTL" envDefault:"30m"`
	Workers      int           `env:"PENDING_WORKERS" envDefault:"2"`
	PollInterval time.Duration `env:"PENDING_POLL_INTERVAL" envDefault:"500ms"`
	Enabled      bool          `env:"ENABLE_REFERENCE_WORKER" envDefault:"true"`
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
	TxTimeout        time.Duration `env:"DB_TX_TIMEOUT" envDefault:"15s"`
}

type AWS struct {
	Region      string `env:"AWS_REGION,required,notEmpty"`
	EndpointURL string `env:"AWS_ENDPOINT_URL"`
	InputQueue  string `env:"SQS_INPUT_QUEUE" envDefault:"wager-transactions.fifo"`
	EventsQueue string `env:"SQS_EVENTS_QUEUE" envDefault:"wallet-events.fifo"`
	InputDLQ    string `env:"SQS_INPUT_DLQ" envDefault:"wager-transactions-dlq.fifo"`
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
	if c.Database.TxTimeout <= c.Database.StatementTimeout {
		errs = append(errs, errors.New("DB_TX_TIMEOUT must be greater than DB_STATEMENT_TIMEOUT"))
	}
	if c.Pending.BaseBackoff <= 0 || c.Pending.MaxBackoff < c.Pending.BaseBackoff || c.Pending.MaxAttempts < 1 ||
		c.Pending.TTL <= 0 || c.Pending.Workers < 1 || c.Pending.PollInterval <= 0 {
		errs = append(errs, errors.New("PENDING_* settings must be positive and PENDING_MAX_BACKOFF >= PENDING_BASE_BACKOFF"))
	}
	if cs := c.Consumer; cs.Workers < 1 || cs.WaitTime < 0 || cs.WaitTime > 20*time.Second || cs.HandlerTimeout <= 0 ||
		cs.HandlerTimeout >= cs.VisibilityTimeout || cs.RetryBaseDelay <= 0 || cs.RetryMaxDelay < cs.RetryBaseDelay ||
		cs.RetryMaxDelay > 12*time.Hour || cs.Name == "" || len(cs.KnownProviders) == 0 {
		errs = append(errs, errors.New("SQS consumer settings invalid: SQS_WAIT_TIME <= 20s, SQS_HANDLER_TIMEOUT < SQS_VISIBILITY_TIMEOUT, "+
			"0 < SQS_RETRY_BASE_DELAY <= SQS_RETRY_MAX_DELAY <= 12h, SQS_WORKERS >= 1, KNOWN_PROVIDERS not empty"))
	}
	if o := c.Outbox; o.Workers < 1 || o.BatchSize < 1 || o.PollInterval <= 0 || o.PublishTimeout <= 0 ||
		o.Lease <= o.PublishTimeout || o.RetryBaseDelay <= 0 || o.RetryMaxDelay < o.RetryBaseDelay {
		errs = append(errs, errors.New("OUTBOX_* settings invalid: positive values, OUTBOX_LEASE > OUTBOX_PUBLISH_TIMEOUT, "+
			"OUTBOX_RETRY_MAX_DELAY >= OUTBOX_RETRY_BASE_DELAY"))
	}
	for name, queue := range map[string]string{"SQS_INPUT_QUEUE": c.AWS.InputQueue, "SQS_EVENTS_QUEUE": c.AWS.EventsQueue, "SQS_INPUT_DLQ": c.AWS.InputDLQ} {
		if !strings.HasSuffix(queue, ".fifo") {
			errs = append(errs, fmt.Errorf("%s must be a FIFO queue (.fifo): %q", name, queue))
		}
	}
	if c.InstanceID == "" {
		errs = append(errs, errors.New("INSTANCE_ID is empty and hostname is unavailable"))
	}
	return errors.Join(errs...)
}
