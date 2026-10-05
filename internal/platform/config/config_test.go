package config

import (
	"log/slog"
	"strings"
	"testing"
)

func setRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("AWS_REGION", "us-east-1")
}

func TestLoad_AppliesDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.Database.MaxConns != 10 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.AWS.InputQueue != "wager-transactions.fifo" || cfg.AWS.EventsQueue != "wallet-events.fifo" {
		t.Errorf("unexpected queue defaults: %+v", cfg.AWS)
	}
	if cfg.InstanceID == "" {
		t.Error("InstanceID should default to the hostname")
	}
}

func TestLoad_RejectsInvalidConfig(t *testing.T) {
	tests := map[string]struct {
		env  map[string]string
		want string
	}{
		"missing database url": {env: map[string]string{"DATABASE_URL": ""}, want: "DATABASE_URL"},
		"missing aws region":   {env: map[string]string{"AWS_REGION": ""}, want: "AWS_REGION"},
		"zero max conns":       {env: map[string]string{"DB_MAX_CONNS": "0"}, want: "DB_MAX_CONNS"},
		"non fifo queue":       {env: map[string]string{"SQS_INPUT_QUEUE": "wager-transactions"}, want: "SQS_INPUT_QUEUE"},
		"invalid log level":    {env: map[string]string{"LOG_LEVEL": "LOUD"}, want: "LogLevel"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want mention of %s", err, tt.want)
			}
		})
	}
}
