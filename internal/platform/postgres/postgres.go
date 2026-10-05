package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var Module = fx.Module("postgres", fx.Provide(NewPool))

func NewPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolCfg.MaxConns = cfg.Database.MaxConns

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("postgres ping: %w", err)
			}
			log.Info("postgres pool ready", "maxConns", poolCfg.MaxConns)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			log.Info("postgres pool closed")
			return nil
		},
	})
	return pool, nil
}
