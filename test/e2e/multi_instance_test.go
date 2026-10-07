//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
)

func TestMultiInstance_SameBetFiftyTimesAcrossInstances(t *testing.T) {
	e := setup(t)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	id := "e2e-" + uuid.NewString()

	results := make([]response, 50)
	concurrently(50, func(i int) { results[i] = submit(t, e, i, w, "BET", "10.00", id, "") })

	statuses := map[int]int{}
	for _, r := range results {
		statuses[r.Status]++
		if r.Body["transactionId"] != results[0].Body["transactionId"] || r.balance() != "90.00" {
			t.Errorf("divergent response: %d %s", r.Status, r.Raw)
		}
	}
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusOK] != 49 {
		t.Fatalf("statuses = %v, want one 201 and 49 replays (200)", statuses)
	}
	if n := count(t, e.DB, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID); n != 1 {
		t.Errorf("debits = %d, want 1", n)
	}
	assertConverged(t, e)
}

func TestMultiInstance_CompetingBetsOnDifferentInstances(t *testing.T) {
	e := setup(t)
	for round := range 10 {
		w := openWallet(t, e, uuid.NewString(), "100.00")
		ids := [2]string{"e2e-" + uuid.NewString(), "e2e-" + uuid.NewString()}

		var results [2]response
		concurrently(2, func(i int) { results[i] = submit(t, e, round+i, w, "BET", "80.00", ids[i], "") })

		processed, rejected := -1, -1
		for i, r := range results {
			switch {
			case r.Status == http.StatusCreated:
				processed = i
			case r.Status == http.StatusUnprocessableEntity && r.Body["failureCode"] == "INSUFFICIENT_FUNDS":
				rejected = i
			}
		}
		if processed < 0 || rejected < 0 {
			t.Fatalf("round %d: %d %s / %d %s, want one 201 and one 422 INSUFFICIENT_FUNDS",
				round, results[0].Status, results[0].Raw, results[1].Status, results[1].Raw)
		}
		if n := count(t, e.DB, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID); n != 1 {
			t.Errorf("round %d: debits = %d, want 1", round, n)
		}
		if n := count(t, e.DB, `SELECT balance FROM wallets WHERE id = $1`, w.ID); n != 2000 {
			t.Errorf("round %d: balance = %d cents, want 2000", round, n)
		}

		for i, original := range results {
			replay := submit(t, e, round+i+1, w, "BET", "80.00", ids[i], "")
			if replay.Body["idempotentReplay"] != true || replay.Body["transactionId"] != original.Body["transactionId"] ||
				replay.Body["status"] != original.Body["status"] || replay.balance() != original.balance() {
				t.Errorf("round %d: replay = %d %s, want the original %s", round, replay.Status, replay.Raw, original.Raw)
			}
		}
	}
	assertConverged(t, e)
}

func TestMultiInstance_ManyWalletsManyOperations(t *testing.T) {
	e := setup(t)
	const wallets, opsPerWallet = 30, 20

	type op struct {
		wallet         wallet
		kind, amount   string
		externalID     string
		instanceOffset int
	}
	var ops []op
	walletList := make([]wallet, wallets)
	for i := range walletList {
		walletList[i] = openWallet(t, e, uuid.NewString(), "1000.00")
		for j := range opsPerWallet {
			kind, amount := "BET", "10.00"
			if j%2 == 1 {
				kind, amount = "WIN", "5.00"
			}
			ops = append(ops, op{walletList[i], kind, amount, "e2e-" + uuid.NewString(), i + j})
		}
	}

	run := func(wantStatus int) {
		sem := make(chan struct{}, 30)
		var mu sync.Mutex
		unexpected := 0
		concurrently(len(ops), func(i int) {
			sem <- struct{}{}
			defer func() { <-sem }()
			o := ops[i]
			if res := submit(t, e, o.instanceOffset, o.wallet, o.kind, o.amount, o.externalID, ""); res.Status != wantStatus {
				mu.Lock()
				unexpected++
				mu.Unlock()
				t.Errorf("%s %s = %d %s, want %d", o.kind, o.externalID, res.Status, res.Raw, wantStatus)
			}
		})
		if unexpected > 0 {
			t.FailNow()
		}
	}
	assertBalances := func() {
		for _, w := range walletList {
			var balance, version, entries int
			err := e.DB.QueryRow(t.Context(), `SELECT w.balance, w.version, (SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = w.id)
				FROM wallets w WHERE w.id = $1`, w.ID).Scan(&balance, &version, &entries)
			if err != nil {
				t.Fatal(err)
			}
			if balance != 95000 || version != opsPerWallet+1 || entries != opsPerWallet+1 {
				t.Errorf("wallet %s: balance %d, version %d, ledger entries %d; want 95000, %d, %d",
					w.ID, balance, version, entries, opsPerWallet+1, opsPerWallet+1)
			}
		}
	}

	run(http.StatusCreated)
	assertBalances()

	run(http.StatusOK)
	assertBalances()
	assertConverged(t, e)
}

func TestMultiInstance_SameOperationsOverHTTPAndSQS(t *testing.T) {
	e := setup(t)
	client := newSQSClient(t)
	const wallets, opsPerWallet = 6, 10

	type op struct {
		wallet     wallet
		externalID string
		messageID  string
	}
	var ops []op
	for range wallets {
		w := openWallet(t, e, uuid.NewString(), "100.00")
		for range opsPerWallet {
			ops = append(ops, op{w, "e2e-" + uuid.NewString(), "msg-" + uuid.NewString()})
		}
	}

	before := sqsMessagesByInstance(t, e)
	concurrently(len(ops)*2, func(i int) {
		o := ops[i/2]
		if i%2 == 0 {
			if res := submit(t, e, i/2, o.wallet, "BET", "1.00", o.externalID, ""); res.Status != http.StatusCreated && res.Status != http.StatusOK {
				t.Errorf("HTTP %s = %d %s", o.externalID, res.Status, res.Raw)
			}
			return
		}
		data := wagerBody(o.wallet, "BET", "1.00", o.externalID, "")
		data["idempotencyKey"] = "provider-a:" + o.externalID
		body, _ := json.Marshal(map[string]any{"messageId": o.messageID, "type": "WagerTransactionRequested",
			"occurredAt": time.Now().UTC().Format(time.RFC3339), "data": data})
		if _, err := client.SendMessage(t.Context(), &sqs.SendMessageInput{
			QueueUrl: aws.String(client.Queues.InputURL), MessageBody: aws.String(string(body)),
			MessageGroupId: aws.String(o.wallet.ID), MessageDeduplicationId: aws.String(o.messageID),
		}); err != nil {
			t.Errorf("send %s: %v", o.messageID, err)
		}
	})

	messageIDs := make([]string, len(ops))
	for i, o := range ops {
		messageIDs[i] = o.messageID
	}
	waitFor(t, 60*time.Second, "all SQS messages consumed", func() bool {
		return count(t, e.DB, `SELECT count(*) FROM inbox_messages WHERE message_id = ANY($1) AND completed_at IS NOT NULL`, messageIDs) == len(ops)
	})

	for _, o := range ops {
		if n := count(t, e.DB, `SELECT count(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND external_transaction_id = $1`, o.externalID); n != 1 {
			t.Errorf("%s: %d transactions, want 1", o.externalID, n)
		}
	}
	for w := 0; w < len(ops); w += opsPerWallet {
		id := ops[w].wallet.ID
		if n := count(t, e.DB, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, id); n != opsPerWallet {
			t.Errorf("wallet %s: %d debits, want %d", id, n, opsPerWallet)
		}
		if n := count(t, e.DB, `SELECT balance FROM wallets WHERE id = $1`, id); n != 10000-opsPerWallet*100 {
			t.Errorf("wallet %s: balance %d cents, want %d", id, n, 10000-opsPerWallet*100)
		}
	}

	after := sqsMessagesByInstance(t, e)
	for i, base := range e.Instances {
		t.Logf("%s consumed %v SQS messages", base, after[i]-before[i])
	}

	for i, o := range ops {
		if res := submit(t, e, i+1, o.wallet, "BET", "1.00", o.externalID, ""); res.Status != http.StatusOK {
			t.Errorf("HTTP replay %s = %d %s", o.externalID, res.Status, res.Raw)
		}
	}
	assertConverged(t, e)
}

func TestMultiInstance_PendingReferencesResumedByAnyInstance(t *testing.T) {
	e := setup(t)
	type pending struct {
		wallet      wallet
		betID, txID string
	}
	items := make([]pending, 9)
	for i := range items {
		w := openWallet(t, e, uuid.NewString(), "100.00")
		betID := "e2e-" + uuid.NewString()
		res := submit(t, e, i, w, "REFUND", "30.00", "e2e-"+uuid.NewString(), betID)
		if res.Status != http.StatusAccepted {
			t.Fatalf("refund before its bet = %d %s", res.Status, res.Raw)
		}
		items[i] = pending{w, betID, res.Body["transactionId"].(string)}
	}

	concurrently(len(items), func(i int) {
		if res := submit(t, e, i+1, items[i].wallet, "BET", "30.00", items[i].betID, ""); res.Status != http.StatusCreated {
			t.Errorf("referenced bet = %d %s", res.Status, res.Raw)
		}
	})

	for i, it := range items {
		waitFor(t, 30*time.Second, fmt.Sprintf("pending refund %s", it.txID), func() bool {
			res := doRequest(t, http.MethodGet, e.instance(i+2)+"/wagering/transactions/"+it.txID, e.ProviderA, nil, nil)
			return res.Body["status"] == "PROCESSED"
		})
		if n := count(t, e.DB, `SELECT balance FROM wallets WHERE id = $1`, it.wallet.ID); n != 10000 {
			t.Errorf("wallet %s: balance %d cents, want 10000 after BET and REFUND", it.wallet.ID, n)
		}
	}
	assertConverged(t, e)
}

func sqsMessagesByInstance(t *testing.T, e env) []float64 {
	t.Helper()
	totals := make([]float64, len(e.Instances))
	for i, base := range e.Instances {
		resp, err := http.Get(base + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "sqs_messages_total{") {
				v, _ := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64)
				totals[i] += v
			}
		}
	}
	return totals
}
