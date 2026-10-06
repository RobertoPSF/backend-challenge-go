package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReady_ReportsDrainingDuringShutdown(t *testing.T) {
	h := &Health{}
	h.draining.Store(true)

	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while draining", rec.Code)
	}
}
