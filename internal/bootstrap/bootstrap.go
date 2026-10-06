package bootstrap

import (
	"time"

	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/auth"
	"github.com/RobertoPSF/backend-challenge-go/internal/httpapi"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/logger"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/postgres"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
)

const (
	StartTimeout = 30 * time.Second
	StopTimeout  = 30 * time.Second
)

func Options() fx.Option {
	return fx.Options(
		fx.StartTimeout(StartTimeout),
		fx.StopTimeout(StopTimeout),
		fx.WithLogger(logger.FxEventLogger),

		config.Module,
		logger.Module,
		metrics.Module,
		postgres.Module,
		sqsclient.Module,
		store.Module,
		auth.Module,
		app.Module,
		httpapi.Module,
	)
}
