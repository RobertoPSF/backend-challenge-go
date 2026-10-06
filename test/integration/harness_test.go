//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx/fxtest"

	"github.com/RobertoPSF/backend-challenge-go/internal/bootstrap"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

type testApp struct {
	BaseURL string
	KC      testinfra.Keycloak
	PG      testinfra.Postgres
	DB      *pgx.Conn
}

func startApp(t *testing.T) testApp {
	t.Helper()
	pg := testinfra.StartPostgres(t)
	ls := testinfra.StartLocalStack(t)
	kc := testinfra.StartKeycloak(t)
	addr := testinfra.FreeAddr(t)

	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("DATABASE_URL", pg.AppURL)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_ENDPOINT_URL", ls.Endpoint)
	t.Setenv("OIDC_ISSUER", kc.Issuer)
	t.Setenv("OIDC_JWKS_URL", kc.JWKSURL)
	t.Setenv("LOG_LEVEL", "WARN")

	app := fxtest.New(t, bootstrap.Options())
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	return testApp{BaseURL: "http://" + addr, KC: kc, PG: pg, DB: connect(t, pg.OwnerURL)}
}

type response struct {
	Status int
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

func (r response) errorCode() string {
	e, _ := r.Body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func doRequest(t *testing.T, method, url, token string, body any, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = bytes.NewBufferString(b)
	default:
		raw, _ := json.Marshal(b)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return response{Status: resp.StatusCode, Header: resp.Header, Raw: raw, Body: parsed}
}

func count(t *testing.T, db *pgx.Conn, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
