//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"

	"github.com/RobertoPSF/backend-challenge-go/internal/bootstrap"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

func TestFxApp_StartsServesAndStopsWithoutLeaks(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	ls := testinfra.StartLocalStack(t)
	addr := testinfra.FreeAddr(t)

	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("DATABASE_URL", pg.AppURL)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_ENDPOINT_URL", ls.Endpoint)
	t.Setenv("LOG_LEVEL", "WARN")

	leaks := goleak.IgnoreCurrent()

	app := fxtest.New(t, bootstrap.Options())
	app.RequireStart()

	resp, err := http.Get("http://" + addr + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	http.DefaultClient.CloseIdleConnections()

	if resp.StatusCode != http.StatusOK || body.Checks["postgres"] != "ok" || body.Checks["sqs"] != "ok" {
		t.Fatalf("readiness = %d %+v, want 200 with postgres and sqs ok", resp.StatusCode, body)
	}

	app.RequireStop()

	goleak.VerifyNone(t, leaks)
}
