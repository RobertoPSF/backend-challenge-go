package worker

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var Module = fx.Module("workers", fx.Invoke(registerPendingReferences))

type PendingReferences struct {
	wagers *app.Wagers
}

func (PendingReferences) Name() string { return "pending-references" }

func (p PendingReferences) RunOnce(ctx context.Context) (bool, error) {
	return p.wagers.ResumeNextPending(ctx)
}

func registerPendingReferences(lc fx.Lifecycle, cfg config.Config, wagers *app.Wagers, log *slog.Logger) {
	if !cfg.Pending.Enabled {
		log.Info("pending references worker disabled")
		return
	}
	runner := NewRunner(PendingReferences{wagers: wagers}, cfg.Pending.Workers, cfg.Pending.PollInterval, log)
	lc.Append(fx.Hook{OnStart: runner.Start, OnStop: runner.Stop})
}
