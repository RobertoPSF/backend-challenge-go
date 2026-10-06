//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
)

func openWalletBody(playerID, amount, currency string) map[string]any {
	return map[string]any{"playerId": playerID, "initialBalance": map[string]any{"amount": amount, "currency": currency}}
}

func TestWalletAPI(t *testing.T) {
	a := startApp(t)
	admin := a.KC.Token(t, "wallet-service")
	provider := a.KC.Token(t, "provider-a")

	t.Run("open with positive balance commits wallet, opening, ledger and events together", func(t *testing.T) {
		playerID := uuid.NewString()
		res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(playerID, "1000.00", "BRL"),
			map[string]string{"X-Correlation-Id": "corr-open-1"})
		if res.Status != http.StatusCreated {
			t.Fatalf("status = %d %s", res.Status, res.Raw)
		}
		walletID := res.Body["id"].(string)
		balance := res.Body["balance"].(map[string]any)
		if res.Body["playerId"] != playerID || balance["amount"] != "1000.00" || balance["currency"] != "BRL" || res.Body["version"] != float64(1) {
			t.Errorf("body = %s", res.Raw)
		}
		if res.Header.Get("Location") != "/wallets/"+walletID || res.Header.Get("X-Correlation-Id") != "corr-open-1" {
			t.Errorf("headers = %v", res.Header)
		}

		if n := count(t, a.DB, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'
			AND status = 'PROCESSED' AND origin = 'INTERNAL' AND amount = 100000 AND balance_after = 100000`, walletID); n != 1 {
			t.Errorf("opening transactions = %d, want 1", n)
		}
		if n := count(t, a.DB, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT'
			AND balance_before = 0 AND balance_after = 100000`, walletID); n != 1 {
			t.Errorf("ledger entries = %d, want 1", n)
		}
		if n := count(t, a.DB, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND correlation_id = 'corr-open-1'
			AND event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged') AND published_at IS NULL`, walletID); n != 2 {
			t.Errorf("outbox events = %d, want 2", n)
		}
		var sameTx bool
		err := a.DB.QueryRow(context.Background(), `SELECT count(DISTINCT tx) = 1 FROM (
			SELECT xmin::text AS tx FROM wallets WHERE id = $1
			UNION ALL SELECT xmin::text FROM wager_transactions WHERE wallet_id = $1
			UNION ALL SELECT xmin::text FROM wallet_ledger_entries WHERE wallet_id = $1
			UNION ALL SELECT xmin::text FROM outbox_events WHERE aggregate_id = $1) rows`, walletID).Scan(&sameTx)
		if err != nil {
			t.Fatal(err)
		}
		if !sameTx {
			t.Error("wallet, opening, ledger and outbox rows were not written by the same database transaction")
		}
	})

	t.Run("open with zero balance creates no financial records", func(t *testing.T) {
		res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(uuid.NewString(), "0.00", "BRL"), nil)
		if res.Status != http.StatusCreated || res.Body["balance"].(map[string]any)["amount"] != "0.00" {
			t.Fatalf("status = %d %s", res.Status, res.Raw)
		}
		walletID := res.Body["id"]
		for _, table := range []string{"wager_transactions", "wallet_ledger_entries"} {
			if n := count(t, a.DB, "SELECT count(*) FROM "+table+" WHERE wallet_id = $1", walletID); n != 0 {
				t.Errorf("%s rows = %d, want 0", table, n)
			}
		}
		if n := count(t, a.DB, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", walletID); n != 0 {
			t.Errorf("outbox rows = %d, want 0", n)
		}
	})

	t.Run("same player and currency conflicts; another currency does not", func(t *testing.T) {
		playerID := uuid.NewString()
		if res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(playerID, "10.00", "BRL"), nil); res.Status != http.StatusCreated {
			t.Fatalf("first open = %d", res.Status)
		}
		res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(playerID, "10.00", "BRL"), nil)
		if res.Status != http.StatusConflict || res.errorCode() != "WALLET_ALREADY_EXISTS" {
			t.Fatalf("duplicate = %d %s", res.Status, res.Raw)
		}
		if res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(playerID, "10.00", "USD"), nil); res.Status != http.StatusCreated {
			t.Fatalf("other currency = %d %s", res.Status, res.Raw)
		}
		if n := count(t, a.DB, "SELECT count(*) FROM wallets WHERE player_id = $1", playerID); n != 2 {
			t.Errorf("wallets = %d, want 2", n)
		}
	})

	invalid := map[string]struct {
		body any
		code string
	}{
		"malformed json":          {`{"playerId":`, "INVALID_REQUEST"},
		"unknown field":           {`{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"1.00","currency":"BRL"},"extra":1}`, "INVALID_REQUEST"},
		"two json objects":        {`{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"1.00","currency":"BRL"}}{}`, "INVALID_REQUEST"},
		"missing player":          {map[string]any{"initialBalance": map[string]any{"amount": "1.00", "currency": "BRL"}}, "INVALID_REQUEST"},
		"invalid player":          {openWalletBody("player-1", "1.00", "BRL"), "INVALID_REQUEST"},
		"missing initial balance": {map[string]any{"playerId": uuid.NewString()}, "INVALID_REQUEST"},
		"amount without scale":    {openWalletBody(uuid.NewString(), "10", "BRL"), "INVALID_MONEY"},
		"negative amount":         {openWalletBody(uuid.NewString(), "-1.00", "BRL"), "INVALID_MONEY"},
		"scientific notation":     {openWalletBody(uuid.NewString(), "1e3", "BRL"), "INVALID_MONEY"},
		"numeric amount":          {`{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":10.5,"currency":"BRL"}}`, "INVALID_REQUEST"},
		"lowercase currency":      {openWalletBody(uuid.NewString(), "1.00", "brl"), "INVALID_CURRENCY"},
	}
	for name, tc := range invalid {
		t.Run("400 "+name, func(t *testing.T) {
			before := count(t, a.DB, "SELECT count(*) FROM wallets")
			res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, tc.body, nil)
			if res.Status != http.StatusBadRequest || res.errorCode() != tc.code {
				t.Fatalf("got %d %s, want 400 %s", res.Status, res.Raw, tc.code)
			}
			if after := count(t, a.DB, "SELECT count(*) FROM wallets"); after != before {
				t.Errorf("wallets changed from %d to %d", before, after)
			}
		})
	}

	t.Run("wallet routes are restricted to the internal service", func(t *testing.T) {
		before := count(t, a.DB, "SELECT count(*) FROM wallets")
		body := openWalletBody(uuid.NewString(), "50.00", "BRL")

		if res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", "", body, nil); res.Status != http.StatusUnauthorized {
			t.Errorf("no token = %d", res.Status)
		}
		if res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", provider, body, nil); res.Status != http.StatusForbidden {
			t.Errorf("provider token = %d", res.Status)
		}
		if after := count(t, a.DB, "SELECT count(*) FROM wallets"); after != before {
			t.Errorf("unauthorized calls created wallets: %d -> %d", before, after)
		}

		existing := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(uuid.NewString(), "5.00", "BRL"), nil).Body["id"].(string)
		for _, path := range []string{"/wallets/" + existing, "/wallets/" + existing + "/ledger"} {
			if res := doRequest(t, http.MethodGet, a.BaseURL+path, provider, nil, nil); res.Status != http.StatusForbidden || len(res.Body) != 1 {
				t.Errorf("provider GET %s = %d %s", path, res.Status, res.Raw)
			}
			if res := doRequest(t, http.MethodGet, a.BaseURL+path, "", nil, nil); res.Status != http.StatusUnauthorized {
				t.Errorf("anonymous GET %s = %d", path, res.Status)
			}
		}
	})

	t.Run("get wallet", func(t *testing.T) {
		created := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(uuid.NewString(), "75.50", "EUR"), nil)
		id := created.Body["id"].(string)

		res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+id, admin, nil, nil)
		if res.Status != http.StatusOK || res.Body["id"] != id || res.Body["balance"].(map[string]any)["amount"] != "75.50" {
			t.Fatalf("get = %d %s", res.Status, res.Raw)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+uuid.NewString(), admin, nil, nil); res.Status != http.StatusNotFound || res.errorCode() != "NOT_FOUND" {
			t.Errorf("unknown wallet = %d %s", res.Status, res.Raw)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/not-a-uuid", admin, nil, nil); res.Status != http.StatusBadRequest {
			t.Errorf("invalid id = %d", res.Status)
		}
	})

	t.Run("ledger pagination with opaque cursor", func(t *testing.T) {
		created := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(uuid.NewString(), "100.00", "BRL"), nil)
		walletID := uuid.MustParse(created.Body["id"].(string))
		addBets(t, a, walletID, 4)

		var items []map[string]any
		cursor := ""
		for pages := 0; ; pages++ {
			u := a.BaseURL + "/wallets/" + walletID.String() + "/ledger?limit=2"
			if cursor != "" {
				u += "&cursor=" + url.QueryEscape(cursor)
			}
			res := doRequest(t, http.MethodGet, u, admin, nil, nil)
			if res.Status != http.StatusOK {
				t.Fatalf("page %d = %d %s", pages, res.Status, res.Raw)
			}
			for _, it := range res.Body["items"].([]any) {
				items = append(items, it.(map[string]any))
			}
			next, _ := res.Body["nextCursor"].(string)
			if next == "" {
				if res.Body["nextCursor"] != nil {
					t.Errorf("last page nextCursor = %v, want null", res.Body["nextCursor"])
				}
				break
			}
			cursor = next
			if pages > 5 {
				t.Fatal("pagination did not terminate")
			}
		}
		if len(items) != 5 {
			t.Fatalf("items = %d, want 5", len(items))
		}
		if items[0]["direction"] != "CREDIT" || items[0]["balanceAfter"].(map[string]any)["amount"] != "100.00" {
			t.Errorf("first entry = %v", items[0])
		}
		if last := items[4]["balanceAfter"].(map[string]any)["amount"]; last != "96.00" {
			t.Errorf("last balanceAfter = %v, want 96.00", last)
		}

		all := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+walletID.String()+"/ledger", admin, nil, nil)
		if len(all.Body["items"].([]any)) != 5 || all.Body["nextCursor"] != nil {
			t.Errorf("default page = %s", all.Raw)
		}
	})

	t.Run("ledger rejects invalid parameters and unknown wallets", func(t *testing.T) {
		created := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(uuid.NewString(), "1.00", "BRL"), nil)
		base := a.BaseURL + "/wallets/" + created.Body["id"].(string) + "/ledger"
		for _, q := range []string{"?limit=0", "?limit=201", "?limit=abc", "?cursor=garbage"} {
			if res := doRequest(t, http.MethodGet, base+q, admin, nil, nil); res.Status != http.StatusBadRequest {
				t.Errorf("%s = %d", q, res.Status)
			}
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+uuid.NewString()+"/ledger", admin, nil, nil); res.Status != http.StatusNotFound {
			t.Errorf("unknown wallet ledger = %d", res.Status)
		}
	})

	t.Run("correlation id is generated when absent and echoed in errors", func(t *testing.T) {
		res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+uuid.NewString(), admin, nil, nil)
		generated := res.Header.Get("X-Correlation-Id")
		if _, err := uuid.Parse(generated); err != nil {
			t.Fatalf("generated correlation id = %q", generated)
		}
		if res.Body["error"].(map[string]any)["correlationId"] != generated {
			t.Errorf("error body = %s", res.Raw)
		}
	})
}

func addBets(t *testing.T, a testApp, walletID uuid.UUID, n int) {
	t.Helper()
	env := newStoreOn(t, a.PG.AppURL, 5*time.Second)
	ctx := context.Background()
	err := env.store.InTx(ctx, func(r *store.Repos) error {
		w, err := r.Wallets.GetForUpdate(ctx, walletID)
		if err != nil {
			return err
		}
		for range n {
			ext := domain.ExternalDetails{ProviderID: "provider-a", ExternalTransactionID: uuid.NewString(),
				IdempotencyKey: uuid.NewString(), PayloadHash: "h", RoundID: "r", GameID: "g"}
			tx, err := domain.NewExternalTransaction(domain.KindBet, w.ID(), w.PlayerID(), brl(t, "1.00"), ext, time.Now())
			if err != nil {
				return err
			}
			expected := w.Version()
			entry, err := w.Debit(tx.ID(), tx.Money(), time.Now())
			if err != nil {
				return err
			}
			if err := tx.MarkProcessed(w.Balance(), nil, time.Now()); err != nil {
				return err
			}
			if err := r.Transactions.Insert(ctx, tx, "corr"); err != nil {
				return err
			}
			if err := r.Ledger.Insert(ctx, entry); err != nil {
				return err
			}
			if err := r.Wallets.UpdateBalance(ctx, w, expected); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("add bets: %v", err)
	}
}
