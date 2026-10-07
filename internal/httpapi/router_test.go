package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
)

func TestRouter_EveryBusinessRouteRequiresAuthentication(t *testing.T) {
	reg := prometheus.NewRegistry()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(reg, metrics.New(reg), nil, nil, nil, nil, log)
	public := map[string]bool{"/health/live": true, "/health/ready": true, "/metrics": true}

	routes := 0
	err := chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if public[route] {
			return nil
		}
		routes++
		path := strings.NewReplacer("{walletId}", "0192f291-27dd-7d3f-8071-5f8685deef37",
			"{transactionId}", "0192f291-27dd-7d3f-8071-5f8685deef37",
			"{providerId}", "provider-a", "{externalTransactionId}", "tx-1").Replace(route)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", method, route, rec.Code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if routes != 7 {
		t.Errorf("walked %d business routes, want 7; update this test when routes change", routes)
	}
}
