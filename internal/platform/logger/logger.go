package logger

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var Module = fx.Module("logger", fx.Provide(New))

func New(cfg config.Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	return slog.New(handler).With("instanceId", cfg.InstanceID)
}

func FxEventLogger(log *slog.Logger) fxevent.Logger {
	l := &fxevent.SlogLogger{Logger: log}
	l.UseLogLevel(slog.LevelDebug)
	l.UseErrorLevel(slog.LevelError)
	return l
}
