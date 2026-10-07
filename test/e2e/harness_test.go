//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx/fxtest"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
)

type env struct {
	Instances []string
	Admin     string
	ProviderA string
	DB        *pgx.Conn
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func setup(t *testing.T) env {
	t.Helper()
	instances := strings.Split(getenv("E2E_APP_URLS", "http://localhost:8080,http://localhost:8082,http://localhost:8083"), ",")
	if len(instances) < 3 {
		t.Fatalf("E2E_APP_URLS must list at least three instances, got %v", instances)
	}
	for _, base := range instances {
		resp, err := http.Get(base + "/health/ready")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("instance %s is not ready (run `make e2e-up` first): %v", base, err)
		}
		resp.Body.Close()
	}

	db, err := pgx.Connect(t.Context(), getenv("E2E_DATABASE_URL", "postgres://wallet_owner:wallet_owner_local@localhost:5432/wallet?sslmode=disable"))
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })

	return env{Instances: instances, Admin: token(t, "wallet-service"), ProviderA: token(t, "provider-a"), DB: db}
}

func (e env) instance(i int) string { return e.Instances[i%len(e.Instances)] }

func token(t *testing.T, clientID string) string {
	t.Helper()
	tokenURL := getenv("E2E_KEYCLOAK_URL", "http://localhost:8081") + "/realms/wagering/protocol/openid-connect/token"
	req, _ := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientID+"-local-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token for %s: %v", clientID, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("token for %s: status %d", clientID, resp.StatusCode)
	}
	return body.AccessToken
}

type response struct {
	Status int
	Raw    []byte
	Body   map[string]any
}

func (r response) balance() string {
	b, _ := r.Body["balance"].(map[string]any)
	amount, _ := b["amount"].(string)
	return amount
}

func doRequest(t *testing.T, method, url, token string, body any, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("%s %s: %v", method, url, err)
		return response{}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return response{Status: resp.StatusCode, Raw: raw, Body: parsed}
}

type wallet struct {
	ID       string
	PlayerID string
}

func openWallet(t *testing.T, e env, playerID, amount string) wallet {
	t.Helper()
	res := doRequest(t, http.MethodPost, e.instance(0)+"/wallets", e.Admin,
		map[string]any{"playerId": playerID, "initialBalance": map[string]any{"amount": amount, "currency": "BRL"}}, nil)
	if res.Status != http.StatusCreated {
		t.Fatalf("open wallet = %d %s", res.Status, res.Raw)
	}
	return wallet{ID: res.Body["id"].(string), PlayerID: playerID}
}

func wagerBody(w wallet, kind, amount, externalID, reference string) map[string]any {
	body := map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID,
		"playerId": w.PlayerID, "walletId": w.ID, "roundId": "round-e2e", "gameId": "fortune-chimp",
		"kind": kind, "money": map[string]any{"amount": amount, "currency": "BRL"},
	}
	if reference != "" {
		body["referenceExternalTransactionId"] = reference
	}
	return body
}

func submit(t *testing.T, e env, instance int, w wallet, kind, amount, externalID, reference string) response {
	t.Helper()
	return doRequest(t, http.MethodPost, e.instance(instance)+"/wagering/transactions", e.ProviderA,
		wagerBody(w, kind, amount, externalID, reference),
		map[string]string{"Idempotency-Key": "provider-a:" + externalID})
}

func concurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			fn(i)
		})
	}
	close(start)
	wg.Wait()
}

func count(t *testing.T, db *pgx.Conn, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func waitFor(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func assertConverged(t *testing.T, e env) {
	t.Helper()
	waitFor(t, 30*time.Second, "outbox drained", func() bool {
		return count(t, e.DB, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	var wallets, divergent int
	err := e.DB.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE w.balance <> COALESCE(l.net, 0)) FROM wallets w
		LEFT JOIN (SELECT wallet_id, sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END) AS net
		           FROM wallet_ledger_entries GROUP BY wallet_id) l ON l.wallet_id = w.id`).Scan(&wallets, &divergent)
	if err != nil {
		t.Fatal(err)
	}
	if divergent != 0 {
		t.Fatalf("%d of %d wallets have a stored balance different from ledger credits - debits", divergent, wallets)
	}
}

func newSQSClient(t *testing.T) *sqsclient.Client {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg := config.Config{AWS: config.AWS{Region: "us-east-1", EndpointURL: getenv("E2E_SQS_URL", "http://localhost:4566"),
		InputQueue: "wager-transactions.fifo", EventsQueue: "wallet-events.fifo", InputDLQ: "wager-transactions-dlq.fifo"}}
	lc := fxtest.NewLifecycle(t)
	client, err := sqsclient.New(lc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	lc.RequireStart()
	t.Cleanup(lc.RequireStop)
	return client
}
