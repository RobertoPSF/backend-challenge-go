package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var Module = fx.Module("http",
	fx.Provide(NewHealth, NewRouter, NewServer),
	fx.Invoke(func(*http.Server) {}),
)

func NewServer(lc fx.Lifecycle, shutdowner fx.Shutdowner, cfg config.Config, handler http.Handler, log *slog.Logger) *http.Server {
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("http listen %s: %w", srv.Addr, err)
			}
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("http server failed", "error", err)
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()
			log.Info("http server started", "addr", ln.Addr().String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			err := srv.Shutdown(ctx)
			log.Info("http server stopped", "error", err)
			return err
		},
	})
	return srv
}
