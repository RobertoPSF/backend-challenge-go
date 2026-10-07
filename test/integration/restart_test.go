//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
)

func waitFor(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestRestart_PreservesIdempotencyPendingWorkAndConsistency(t *testing.T) {
	infra := startInfra(t)
	db := connect(t, infra.PG.OwnerURL)
	admin := infra.KC.Token(t, "wallet-service")
	provider := infra.KC.Token(t, "provider-a")
	ext := func() string { return "tx-" + uuid.NewString() }

	baseA, stopA := infra.startInstance(t, "instance-a")
	a := testApp{BaseURL: baseA, KC: infra.KC, PG: infra.PG, LS: infra.LS, DB: db}

	w := openAPIWallet(t, a, admin, "100.00")
	betID, laterBetID := ext(), ext()
	first := submit(t, a, provider, "provider-a", w, "BET", "30.00", betID, "")
	if first.Status != http.StatusCreated || balanceOf(first) != "70.00" {
		t.Fatalf("bet on instance A = %d %s", first.Status, first.Raw)
	}
	pending := submit(t, a, provider, "provider-a", w, "REFUND", "20.00", ext(), laterBetID)
	if pending.Status != http.StatusAccepted {
		t.Fatalf("refund on instance A = %d %s", pending.Status, pending.Raw)
	}
	pendingTx := pending.Body["transactionId"].(string)

	stopped := make(chan struct{})
	go func() {
		stopA()
		close(stopped)
	}()
	time.Sleep(300 * time.Millisecond)

	sqsID, msgID := ext(), "msg-"+uuid.NewString()
	data := wagerBody("provider-a", w, "BET", "10.00", sqsID, "")
	data["idempotencyKey"] = "provider-a:" + sqsID
	body, _ := json.Marshal(map[string]any{"messageId": msgID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339), "data": data})
	sqsClient := newSQSClient(t, infra.LS.Endpoint)
	if _, err := sqsClient.SendMessage(t.Context(), &sqs.SendMessageInput{
		QueueUrl: aws.String(sqsClient.Queues.InputURL), MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(msgID),
	}); err != nil {
		t.Fatal(err)
	}

	<-stopped

	baseB, _ := infra.startInstance(t, "instance-b")
	b := testApp{BaseURL: baseB, KC: infra.KC, PG: infra.PG, LS: infra.LS, DB: db}

	t.Run("idempotency survives the restart and replays the original result", func(t *testing.T) {
		replay := submit(t, b, provider, "provider-a", w, "BET", "30.00", betID, "")
		if replay.Status != http.StatusOK || replay.Body["idempotentReplay"] != true ||
			replay.Body["transactionId"] != first.Body["transactionId"] || balanceOf(replay) != "70.00" {
			t.Fatalf("replay on instance B = %d %s", replay.Status, replay.Raw)
		}
	})

	t.Run("a message sent while instance A was shutting down is released and consumed by B", func(t *testing.T) {
		waitFor(t, 15*time.Second, "SQS message", func() bool {
			return count(t, db, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PROCESSED'`, sqsID) == 1
		})
	})

	t.Run("pending work created by instance A is resumed by instance B", func(t *testing.T) {
		if res := submit(t, b, provider, "provider-a", w, "BET", "20.00", laterBetID, ""); res.Status != http.StatusCreated {
			t.Fatalf("referenced bet on instance B = %d %s", res.Status, res.Raw)
		}
		waitFor(t, 15*time.Second, "pending refund", func() bool {
			view := doRequest(t, http.MethodGet, b.BaseURL+"/wagering/transactions/"+pendingTx, provider, nil, nil)
			return view.Body["status"] == "PROCESSED"
		})
	})

	t.Run("financial consistency and event publication are preserved", func(t *testing.T) {
		res := doRequest(t, http.MethodPost, b.BaseURL+"/wallets/"+w.ID+"/reconciliation", admin, nil, nil)
		stored, _ := res.Body["storedBalance"].(map[string]any)
		if res.Body["consistent"] != true || stored["amount"] != "60.00" || res.Body["checkedEntries"] != float64(5) {
			t.Fatalf("reconciliation = %s, want 60.00 consistent over 5 entries (opening, 3 bets, refund)", res.Raw)
		}
		waitFor(t, 15*time.Second, "outbox drained", func() bool {
			return count(t, db, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
		})
		assertAllWalletsReconcile(t, db)
	})
}
