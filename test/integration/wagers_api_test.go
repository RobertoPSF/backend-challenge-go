//go:build integration

package integration

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type apiWallet struct {
	ID       string
	PlayerID string
}

func openAPIWallet(t *testing.T, a testApp, admin, amount string) apiWallet {
	t.Helper()
	player := uuid.NewString()
	res := doRequest(t, http.MethodPost, a.BaseURL+"/wallets", admin, openWalletBody(player, amount, "BRL"), nil)
	if res.Status != http.StatusCreated {
		t.Fatalf("open wallet = %d %s", res.Status, res.Raw)
	}
	return apiWallet{ID: res.Body["id"].(string), PlayerID: player}
}

func wagerBody(provider string, w apiWallet, kind, amount, externalID, reference string) map[string]any {
	body := map[string]any{
		"providerId": provider, "externalTransactionId": externalID,
		"playerId": w.PlayerID, "walletId": w.ID, "roundId": "round-987", "gameId": "fortune-chimp",
		"kind": kind, "money": map[string]any{"amount": amount, "currency": "BRL"},
	}
	if reference != "" {
		body["referenceExternalTransactionId"] = reference
	}
	return body
}

func submit(t *testing.T, a testApp, token, provider string, w apiWallet, kind, amount, externalID, reference string) response {
	t.Helper()
	return doRequest(t, http.MethodPost, a.BaseURL+"/wagering/transactions", token,
		wagerBody(provider, w, kind, amount, externalID, reference),
		map[string]string{"Idempotency-Key": provider + ":" + externalID})
}

func balanceOf(r response) string {
	b, _ := r.Body["balance"].(map[string]any)
	amount, _ := b["amount"].(string)
	return amount
}

func TestWagerAPI(t *testing.T) {
	a := startApp(t)
	admin := a.KC.Token(t, "wallet-service")
	providerA := a.KC.Token(t, "provider-a")
	providerB := a.KC.Token(t, "provider-b")
	ext := func() string { return "tx-" + uuid.NewString() }

	t.Run("BET is processed (201) and replayed (200) with the original balance", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "1000.00")
		id := ext()
		res := submit(t, a, providerA, "provider-a", w, "BET", "25.00", id, "")
		if res.Status != http.StatusCreated || res.Body["status"] != "PROCESSED" || balanceOf(res) != "975.00" || res.Body["idempotentReplay"] != false {
			t.Fatalf("first = %d %s", res.Status, res.Raw)
		}
		submit(t, a, providerA, "provider-a", w, "BET", "100.00", ext(), "")

		replay := submit(t, a, providerA, "provider-a", w, "BET", "25.00", id, "")
		if replay.Status != http.StatusOK || replay.Body["idempotentReplay"] != true || balanceOf(replay) != "975.00" ||
			replay.Body["transactionId"] != res.Body["transactionId"] {
			t.Fatalf("replay = %d %s", replay.Status, replay.Raw)
		}
	})

	t.Run("business rejection is 422 with failure code and observed balance, also on replay", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "10.00")
		id := ext()
		for i, wantReplay := range []bool{false, true} {
			res := submit(t, a, providerA, "provider-a", w, "BET", "80.00", id, "")
			if res.Status != http.StatusUnprocessableEntity || res.Body["status"] != "REJECTED" ||
				res.Body["failureCode"] != "INSUFFICIENT_FUNDS" || balanceOf(res) != "10.00" || res.Body["idempotentReplay"] != wantReplay {
				t.Fatalf("attempt %d = %d %s", i, res.Status, res.Raw)
			}
		}
	})

	t.Run("LOSS is processed without changing the balance", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "10.00")
		res := submit(t, a, providerA, "provider-a", w, "LOSS", "0.00", ext(), "")
		if res.Status != http.StatusCreated || balanceOf(res) != "10.00" {
			t.Fatalf("loss = %d %s", res.Status, res.Raw)
		}
	})

	t.Run("reversal before its reference is accepted as pending (202) and trackable", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		res := submit(t, a, providerA, "provider-a", w, "REFUND", "30.00", ext(), "bet-not-yet")
		if res.Status != http.StatusAccepted || res.Body["status"] != "PENDING_REFERENCE" || res.Body["balance"] != nil {
			t.Fatalf("pending = %d %s", res.Status, res.Raw)
		}
		location := res.Header.Get("Location")
		view := doRequest(t, http.MethodGet, a.BaseURL+location, providerA, nil, nil)
		if view.Status != http.StatusOK || view.Body["status"] != "PENDING_REFERENCE" || view.Body["nextAttemptAt"] == nil ||
			view.Body["expiresAt"] == nil || view.Body["referenceExternalTransactionId"] != "bet-not-yet" {
			t.Fatalf("view = %d %s", view.Status, view.Raw)
		}
	})

	t.Run("the running worker resolves a pending REFUND once its BET arrives", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		bet := ext()
		pending := submit(t, a, providerA, "provider-a", w, "REFUND", "30.00", ext(), bet)
		if pending.Status != http.StatusAccepted {
			t.Fatalf("refund = %d %s", pending.Status, pending.Raw)
		}
		if res := submit(t, a, providerA, "provider-a", w, "BET", "30.00", bet, ""); res.Status != http.StatusCreated {
			t.Fatalf("bet = %d %s", res.Status, res.Raw)
		}

		location := a.BaseURL + pending.Header.Get("Location")
		deadline := time.Now().Add(10 * time.Second)
		for {
			view := doRequest(t, http.MethodGet, location, providerA, nil, nil)
			if view.Body["status"] == "PROCESSED" {
				if balanceOf(view) != "100.00" {
					t.Errorf("balance after refund = %s, want 100.00", balanceOf(view))
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("refund still %v after 10s", view.Body["status"])
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("REFUND of a BET through the API", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		bet := ext()
		submit(t, a, providerA, "provider-a", w, "BET", "30.00", bet, "")
		refund := submit(t, a, providerA, "provider-a", w, "REFUND", "30.00", ext(), bet)
		if refund.Status != http.StatusCreated || balanceOf(refund) != "100.00" {
			t.Fatalf("refund = %d %s", refund.Status, refund.Raw)
		}
		second := submit(t, a, providerA, "provider-a", w, "ROLLBACK", "30.00", ext(), bet)
		if second.Status != http.StatusUnprocessableEntity || second.Body["failureCode"] != "ALREADY_REVERSED" {
			t.Fatalf("second reversal = %d %s", second.Status, second.Raw)
		}
	})

	t.Run("idempotency conflicts are 409", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		id := ext()
		submit(t, a, providerA, "provider-a", w, "BET", "10.00", id, "")

		changed := doRequest(t, http.MethodPost, a.BaseURL+"/wagering/transactions", providerA,
			wagerBody("provider-a", w, "BET", "11.00", id, ""), map[string]string{"Idempotency-Key": "provider-a:" + id})
		if changed.Status != http.StatusConflict || changed.errorCode() != "IDEMPOTENCY_KEY_CONFLICT" {
			t.Errorf("changed payload = %d %s", changed.Status, changed.Raw)
		}
		otherKey := doRequest(t, http.MethodPost, a.BaseURL+"/wagering/transactions", providerA,
			wagerBody("provider-a", w, "BET", "10.00", id, ""), map[string]string{"Idempotency-Key": "another-key"})
		if otherKey.Status != http.StatusConflict || otherKey.errorCode() != "EXTERNAL_TRANSACTION_CONFLICT" {
			t.Errorf("other key = %d %s", otherKey.Status, otherKey.Raw)
		}
	})

	t.Run("input errors", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		post := func(body any, headers map[string]string) response {
			return doRequest(t, http.MethodPost, a.BaseURL+"/wagering/transactions", providerA, body, headers)
		}
		key := map[string]string{"Idempotency-Key": "provider-a:x"}

		cases := map[string]struct {
			res    response
			status int
			code   string
		}{
			"missing key":        {post(wagerBody("provider-a", w, "BET", "1.00", "x", ""), nil), 400, "MISSING_IDEMPOTENCY_KEY"},
			"invalid key":        {post(wagerBody("provider-a", w, "BET", "1.00", "x", ""), map[string]string{"Idempotency-Key": "bad key"}), 400, "INVALID_REQUEST"},
			"opening kind":       {post(wagerBody("provider-a", w, "OPENING", "1.00", "x", ""), key), 400, "UNSUPPORTED_KIND"},
			"loss with amount":   {post(wagerBody("provider-a", w, "LOSS", "1.00", "x", ""), key), 400, "INVALID_AMOUNT"},
			"bet with zero":      {post(wagerBody("provider-a", w, "BET", "0.00", "x", ""), key), 400, "INVALID_AMOUNT"},
			"refund without ref": {post(wagerBody("provider-a", w, "REFUND", "1.00", "x", ""), key), 400, "INVALID_REQUEST"},
			"invalid amount":     {post(wagerBody("provider-a", w, "BET", "1e3", "x", ""), key), 400, "INVALID_MONEY"},
			"key in body": {post(func() map[string]any {
				b := wagerBody("provider-a", w, "BET", "1.00", "x", "")
				b["idempotencyKey"] = "provider-a:x"
				return b
			}(), key), 400, "INVALID_REQUEST"},
			"unknown wallet": {post(func() map[string]any {
				b := wagerBody("provider-a", w, "BET", "1.00", "x", "")
				b["walletId"] = uuid.NewString()
				return b
			}(), key), 422, "WALLET_NOT_FOUND"},
		}
		for name, tc := range cases {
			if tc.res.Status != tc.status || tc.res.errorCode() != tc.code {
				t.Errorf("%s = %d %s, want %d %s", name, tc.res.Status, tc.res.Raw, tc.status, tc.code)
			}
		}
		if n := count(t, a.DB, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND origin = 'EXTERNAL'`, w.ID); n != 0 {
			t.Errorf("invalid requests persisted %d transactions", n)
		}
	})

	t.Run("authorization and provider isolation", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		id := ext()
		created := submit(t, a, providerA, "provider-a", w, "BET", "10.00", id, "")
		txID := created.Body["transactionId"].(string)

		if res := submit(t, a, "", "provider-a", w, "BET", "1.00", ext(), ""); res.Status != http.StatusUnauthorized {
			t.Errorf("no token = %d", res.Status)
		}
		if res := submit(t, a, admin, "provider-a", w, "BET", "1.00", ext(), ""); res.Status != http.StatusForbidden {
			t.Errorf("internal service submitting = %d", res.Status)
		}
		if res := submit(t, a, providerB, "provider-a", w, "BET", "1.00", ext(), ""); res.Status != http.StatusForbidden {
			t.Errorf("provider-b posing as provider-a = %d", res.Status)
		}
		if res := submit(t, a, providerB, "provider-a", w, "BET", "10.00", id, ""); res.Status != http.StatusForbidden {
			t.Errorf("provider-b replaying provider-a's request = %d %s", res.Status, res.Raw)
		}

		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wagering/transactions/"+txID, providerB, nil, nil); res.Status != http.StatusNotFound {
			t.Errorf("provider-b reading provider-a by id = %d %s", res.Status, res.Raw)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/providers/provider-a/wagering/transactions/"+id, providerB, nil, nil); res.Status != http.StatusForbidden {
			t.Errorf("provider-b reading provider-a by external id = %d", res.Status)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/providers/provider-b/wagering/transactions/"+id, providerB, nil, nil); res.Status != http.StatusNotFound {
			t.Errorf("provider-b looking up the external id in its own scope = %d", res.Status)
		}

		for _, token := range []string{providerA, admin} {
			byID := doRequest(t, http.MethodGet, a.BaseURL+"/wagering/transactions/"+txID, token, nil, nil)
			byExt := doRequest(t, http.MethodGet, a.BaseURL+"/providers/provider-a/wagering/transactions/"+id, token, nil, nil)
			if byID.Status != http.StatusOK || byExt.Status != http.StatusOK || byExt.Body["transactionId"] != txID ||
				byID.Body["status"] != "PROCESSED" || balanceOf(byID) != "90.00" {
				t.Errorf("authorized reads = %d %s / %d %s", byID.Status, byID.Raw, byExt.Status, byExt.Raw)
			}
		}

		if n := count(t, a.DB, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND origin = 'EXTERNAL'`, w.ID); n != 1 {
			t.Errorf("transactions = %d, want 1 (denied requests must not persist)", n)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+w.ID, admin, nil, nil); balanceOf(res) != "90.00" {
			t.Errorf("balance = %s, want 90.00", balanceOf(res))
		}
	})

	t.Run("reconciliation rebuilds the balance from the ledger without changing it", func(t *testing.T) {
		reconcile := func(walletID, token string) response {
			return doRequest(t, http.MethodPost, a.BaseURL+"/wallets/"+walletID+"/reconciliation", token, nil, nil)
		}
		amountOf := func(r response, field string) string {
			m, _ := r.Body[field].(map[string]any)
			v, _ := m["amount"].(string)
			return v
		}

		w := openAPIWallet(t, a, admin, "1000.00")
		submit(t, a, providerA, "provider-a", w, "BET", "25.00", ext(), "")
		res := reconcile(w.ID, admin)
		if res.Status != http.StatusOK || res.Body["walletId"] != w.ID || amountOf(res, "storedBalance") != "975.00" ||
			amountOf(res, "calculatedBalance") != "975.00" || amountOf(res, "difference") != "0.00" ||
			res.Body["consistent"] != true || res.Body["checkedEntries"] != float64(2) {
			t.Fatalf("README example = %d %s", res.Status, res.Raw)
		}

		empty := openAPIWallet(t, a, admin, "0.00")
		if res := reconcile(empty.ID, admin); res.Body["consistent"] != true || res.Body["checkedEntries"] != float64(0) {
			t.Errorf("zero wallet = %s", res.Raw)
		}

		before := mismatchesMetric(t, a)
		if _, err := a.DB.Exec(t.Context(), `UPDATE wallets SET balance = balance + 1 WHERE id = $1`, w.ID); err != nil {
			t.Fatal(err)
		}
		res = reconcile(w.ID, admin)
		if res.Body["consistent"] != false || amountOf(res, "difference") != "0.01" || amountOf(res, "storedBalance") != "975.01" ||
			amountOf(res, "calculatedBalance") != "975.00" {
			t.Fatalf("tampered = %s", res.Raw)
		}
		if after := mismatchesMetric(t, a); after != before+1 {
			t.Errorf("reconciliation_mismatches_total %v -> %v, want +1", before, after)
		}
		var balance, version int64
		_ = a.DB.QueryRow(t.Context(), `SELECT balance, version FROM wallets WHERE id = $1`, w.ID).Scan(&balance, &version)
		if balance != 97501 || version != 2 {
			t.Errorf("reconciliation changed the wallet: %d v%d", balance, version)
		}

		if res := reconcile(uuid.NewString(), admin); res.Status != http.StatusNotFound {
			t.Errorf("unknown wallet = %d", res.Status)
		}
		if res := reconcile(w.ID, providerA); res.Status != http.StatusForbidden {
			t.Errorf("provider = %d", res.Status)
		}
	})

	t.Run("80.00 + 80.00 over 100.00 through HTTP", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		ids := []string{ext(), ext()}
		results := make([]response, 2)
		runParallelHTTP(2, func(i int) { results[i] = submit(t, a, providerA, "provider-a", w, "BET", "80.00", ids[i], "") })

		statuses := map[int]int{}
		for _, r := range results {
			statuses[r.Status]++
		}
		if statuses[http.StatusCreated] != 1 || statuses[http.StatusUnprocessableEntity] != 1 {
			t.Fatalf("statuses = %v", statuses)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+w.ID, admin, nil, nil); balanceOf(res) != "20.00" {
			t.Errorf("balance = %s, want 20.00", balanceOf(res))
		}
	})

	t.Run("metrics expose the observability signals required by the README", func(t *testing.T) {
		time.Sleep(time.Second)
		resp, err := http.Get(a.BaseURL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(raw)

		for _, series := range []string{
			`wager_transactions_total{channel="http",kind="BET",status="PROCESSED"}`,
			`wager_transactions_total{channel="http",kind="BET",status="REJECTED"}`,
			`wager_transactions_total{channel="worker",kind="REFUND",status="PROCESSED"}`,
			`wager_idempotent_replays_total{channel="http"}`,
			`wager_processing_duration_seconds_count{channel="http"}`,
			`pending_reference_attempts_total{outcome="resolved"}`,
			`outbox_publish_total{result="published"}`,
			`outbox_pending_events `,
			`outbox_oldest_pending_age_seconds `,
			`pending_references_waiting `,
			`reconciliation_mismatches_total `,
			`http_request_duration_seconds_count{method="POST",route="/wagering/transactions",status="201"}`,
		} {
			if !strings.Contains(body, series) {
				t.Errorf("missing metric series %s", series)
			}
		}
	})

	t.Run("the same BET 50 times over HTTP", func(t *testing.T) {
		w := openAPIWallet(t, a, admin, "100.00")
		id := ext()
		results := make([]response, 50)
		runParallelHTTP(50, func(i int) { results[i] = submit(t, a, providerA, "provider-a", w, "BET", "10.00", id, "") })

		statuses := map[int]int{}
		for _, r := range results {
			statuses[r.Status]++
		}
		if statuses[http.StatusCreated] != 1 || statuses[http.StatusOK] != 49 {
			t.Fatalf("statuses = %v, want 1x201 and 49x200", statuses)
		}
		if res := doRequest(t, http.MethodGet, a.BaseURL+"/wallets/"+w.ID, admin, nil, nil); balanceOf(res) != "90.00" {
			t.Errorf("balance = %s, want 90.00", balanceOf(res))
		}
	})
}

func runParallelHTTP(n int, fn func(i int)) {
	start := make(chan struct{})
	done := make(chan struct{}, n)
	for i := range n {
		go func() {
			<-start
			fn(i)
			done <- struct{}{}
		}()
	}
	close(start)
	for range n {
		<-done
	}
}

func mismatchesMetric(t *testing.T, a testApp) float64 {
	t.Helper()
	resp, err := http.Get(a.BaseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "reconciliation_mismatches_total ") {
			v, _ := strconv.ParseFloat(strings.TrimPrefix(line, "reconciliation_mismatches_total "), 64)
			return v
		}
	}
	t.Fatal("reconciliation_mismatches_total not exposed")
	return 0
}
