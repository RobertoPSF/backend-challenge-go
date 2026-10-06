package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
)

func Observe(m *metrics.Metrics, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			elapsed := time.Since(started)
			m.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(status)).Observe(elapsed.Seconds())

			level := slog.LevelInfo
			if strings.HasPrefix(route, "/health") || route == "/metrics" {
				level = slog.LevelDebug
			}
			log.Log(r.Context(), level, "http request",
				"method", r.Method, "route", route, "status", status, "durationMs", elapsed.Milliseconds(),
				"correlationId", correlationFrom(r.Context()))
		})
	}
}
