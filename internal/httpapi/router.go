package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/RobertoPSF/backend-challenge-go/internal/auth"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
)

func NewRouter(reg *prometheus.Registry, m *metrics.Metrics, health *Health, verifier *auth.Verifier, wallets *WalletHandlers, wagers *WagerHandlers, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(CorrelationID, Observe(m, log), middleware.Recoverer)

	r.Get("/health/live", health.Live)
	r.Get("/health/ready", health.Ready)
	r.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	r.Group(func(r chi.Router) {
		r.Use(Authenticate(verifier, log))

		r.Group(func(r chi.Router) {
			r.Use(RequireRole(auth.RoleWalletAdmin))
			r.Post("/wallets", wallets.Open)
			r.Get("/wallets/{walletId}", wallets.Get)
			r.Get("/wallets/{walletId}/ledger", wallets.Ledger)
			r.Post("/wallets/{walletId}/reconciliation", wallets.Reconcile)
		})

		r.With(RequireRole(auth.RoleProvider)).Post("/wagering/transactions", wagers.Submit)

		r.Group(func(r chi.Router) {
			r.Use(RequireRole(auth.RoleProvider, auth.RoleWalletAdmin))
			r.Get("/wagering/transactions/{transactionId}", wagers.Get)
			r.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", wagers.GetByExternalID)
		})
	})

	return r
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
