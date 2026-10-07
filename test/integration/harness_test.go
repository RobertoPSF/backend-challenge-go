//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx/fxtest"

	"github.com/RobertoPSF/backend-challenge-go/internal/bootstrap"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

type testApp struct {
	BaseURL string
	KC      testinfra.Keycloak
	PG      testinfra.Postgres
	LS      testinfra.LocalStack
	DB      *pgx.Conn
}

type testInfra struct {
	PG testinfra.Postgres
	LS testinfra.LocalStack
	KC testinfra.Keycloak
}

func startInfra(t *testing.T) testInfra {
	t.Helper()
	infra := testInfra{PG: testinfra.StartPostgres(t), LS: testinfra.StartLocalStack(t), KC: testinfra.StartKeycloak(t)}
	t.Setenv("DATABASE_URL", infra.PG.AppURL)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_ENDPOINT_URL", infra.LS.Endpoint)
	t.Setenv("OIDC_ISSUER", infra.KC.Issuer)
	t.Setenv("OIDC_JWKS_URL", infra.KC.JWKSURL)
	t.Setenv("LOG_LEVEL", "WARN")
	return infra
}

func (i testInfra) startInstance(t *testing.T, instanceID string) (baseURL string, stop func()) {
	t.Helper()
	addr := testinfra.FreeAddr(t)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("INSTANCE_ID", instanceID)

	app := fxtest.New(t, bootstrap.Options())
	app.RequireStart()
	var once sync.Once
	stop = func() { once.Do(app.RequireStop) }
	t.Cleanup(stop)
	return "http://" + addr, stop
}

func startApp(t *testing.T) testApp {
	t.Helper()
	infra := startInfra(t)
	baseURL, _ := infra.startInstance(t, "app-1")
	return testApp{BaseURL: baseURL, KC: infra.KC, PG: infra.PG, LS: infra.LS, DB: connect(t, infra.PG.OwnerURL)}
}

func newSQSClient(t *testing.T, endpoint string) *sqsclient.Client {
	t.Helper()
	cfg := config.Config{AWS: config.AWS{Region: "us-east-1", EndpointURL: endpoint,
		InputQueue: "wager-transactions.fifo", EventsQueue: "wallet-events.fifo", InputDLQ: "wager-transactions-dlq.fifo"}}
	lc := fxtest.NewLifecycle(t)
	client, err := sqsclient.New(lc, cfg, silentLog)
	if err != nil {
		t.Fatal(err)
	}
	lc.RequireStart()
	t.Cleanup(lc.RequireStop)
	return client
}

func assertAllWalletsReconcile(t *testing.T, db *pgx.Conn) {
	t.Helper()
	var wallets, divergent int
	err := db.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE w.balance <> COALESCE(l.net, 0)) FROM wallets w
		LEFT JOIN (SELECT wallet_id, sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END) AS net
		           FROM wallet_ledger_entries GROUP BY wallet_id) l ON l.wallet_id = w.id`).Scan(&wallets, &divergent)
	if err != nil {
		t.Fatal(err)
	}
	if divergent != 0 {
		t.Fatalf("%d of %d wallets have a stored balance different from ledger credits - debits", divergent, wallets)
	}
	t.Logf("ledger invariant holds for all %d wallets", wallets)
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
