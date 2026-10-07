//go:build faults

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/fault"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
)

func compose(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "-f", "docker-compose.yml", "-f", "docker-compose.e2e.yml"}, args...)...)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func unpause(service string) {
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.yml", "-f", "docker-compose.e2e.yml", "unpause", service)
	cmd.Dir = "../.."
	_ = cmd.Run()
}

func restoreAllInstances(t *testing.T) {
	compose(t, []string{"FAULT_INJECTION_ENABLED=false", "FAULT_POINT="}, "up", "-d", "--wait", "--no-deps", "app", "app2", "app3")
}

// runFaultyAlone leaves app1 as the only running instance, configured to crash at point.
func runFaultyAlone(t *testing.T, point string) {
	t.Helper()
	t.Cleanup(func() { restoreAllInstances(t) })
	compose(t, nil, "stop", "app2", "app3")
	compose(t, []string{"FAULT_INJECTION_ENABLED=true", "FAULT_POINT=" + point}, "up", "-d", "--wait", "--no-deps", "app")
}

func waitCrash(t *testing.T) {
	t.Helper()
	waitFor(t, 30*time.Second, "app1 to crash at the fault point", func() bool {
		return compose(t, nil, "ps", "-a", "--format", "{{.State}} {{.ExitCode}}", "app") == "exited 137"
	})
}

func startRecoveryInstance(t *testing.T) {
	t.Helper()
	compose(t, nil, "up", "-d", "--wait", "--no-deps", "app2")
}

const app1, app2 = 0, 1

func purge(t *testing.T, client *sqsclient.Client, queueURL string) {
	t.Helper()
	if _, err := client.PurgeQueue(t.Context(), &sqs.PurgeQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
		t.Fatal(err)
	}
}

func receiveAll(t *testing.T, client *sqsclient.Client, queueURL string, quiet time.Duration) []types.Message {
	t.Helper()
	var all []types.Message
	deadline := time.Now().Add(quiet)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			all = append(all, m)
			_, _ = client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle})
		}
	}
	return all
}

func eventIDs(t *testing.T, messages []types.Message, walletID string) map[string]int {
	t.Helper()
	ids := map[string]int{}
	for _, m := range messages {
		if m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)] != walletID {
			continue
		}
		var body struct {
			EventID string `json:"eventId"`
		}
		if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &body); err != nil {
			t.Fatal(err)
		}
		ids[body.EventID]++
	}
	return ids
}

func outboxOf(t *testing.T, e env, walletID string) (total, published, reclaimed int) {
	t.Helper()
	err := e.DB.QueryRow(t.Context(), `SELECT count(*), count(published_at), count(*) FILTER (WHERE attempts >= 2)
		FROM outbox_events WHERE aggregate_id = $1`, walletID).Scan(&total, &published, &reclaimed)
	if err != nil {
		t.Fatal(err)
	}
	return total, published, reclaimed
}

func sendWager(t *testing.T, client *sqsclient.Client, w wallet, kind, amount, externalID string) string {
	t.Helper()
	messageID := "msg-" + uuid.NewString()
	data := wagerBody(w, kind, amount, externalID, "")
	data["idempotencyKey"] = "provider-a:" + externalID
	body, _ := json.Marshal(map[string]any{"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339), "data": data})
	if _, err := client.SendMessage(t.Context(), &sqs.SendMessageInput{
		QueueUrl: aws.String(client.Queues.InputURL), MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(messageID),
	}); err != nil {
		t.Fatal(err)
	}
	return messageID
}

func metricOf(t *testing.T, base, series string) float64 {
	t.Helper()
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw strings.Builder
	_, _ = io.Copy(&raw, resp.Body)
	for _, line := range strings.Split(raw.String(), "\n") {
		if strings.HasPrefix(line, series+" ") {
			v, _ := strconv.ParseFloat(strings.TrimPrefix(line, series+" "), 64)
			return v
		}
	}
	return 0
}

func debits(t *testing.T, e env, walletID string) int {
	return count(t, e.DB, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, walletID)
}

func balance(t *testing.T, e env, walletID string) int {
	return count(t, e.DB, `SELECT balance FROM wallets WHERE id = $1`, walletID)
}

func TestFault_ConsumerCrashesAfterCommitBeforeDelete(t *testing.T) {
	e := setup(t)
	client := newSQSClient(t)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	assertConverged(t, e)

	runFaultyAlone(t, fault.ConsumerAfterCommitBeforeDelete)
	id := "e2e-" + uuid.NewString()
	messageID := sendWager(t, client, w, "BET", "25.00", id)
	waitCrash(t)

	if n := count(t, e.DB, `SELECT count(*) FROM inbox_messages WHERE message_id = $1 AND completed_at IS NOT NULL`, messageID); n != 1 {
		t.Fatalf("inbox = %d, want the message committed before the crash", n)
	}
	if debits(t, e, w.ID) != 1 {
		t.Fatalf("debits = %d, want 1 committed before the crash", debits(t, e, w.ID))
	}

	startRecoveryInstance(t)
	waitFor(t, 40*time.Second, "redelivery recognised as a duplicate by the freshly started instance", func() bool {
		return metricOf(t, e.instance(app2), `sqs_messages_total{result="duplicate"}`) == 1
	})

	if debits(t, e, w.ID) != 1 || balance(t, e, w.ID) != 7500 {
		t.Errorf("after redelivery: %d debits, balance %d; want 1 and 7500", debits(t, e, w.ID), balance(t, e, w.ID))
	}
	waitFor(t, 20*time.Second, "input queue empty", func() bool {
		attrs, err := client.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(client.Queues.InputURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		return err == nil && attrs.Attributes["ApproximateNumberOfMessages"] == "0" && attrs.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	assertConverged(t, e)
}

func TestFault_PublisherCrashesBetweenCommitAndPublish(t *testing.T) {
	e := setup(t)
	client := newSQSClient(t)
	assertConverged(t, e)
	purge(t, client, client.Queues.EventsURL)

	runFaultyAlone(t, fault.OutboxAfterClaimBeforePublish)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	waitCrash(t)

	if total, published, _ := outboxOf(t, e, w.ID); total != 2 || published != 0 {
		t.Fatalf("outbox = %d events, %d published; want 2 committed and none published", total, published)
	}
	if early := eventIDs(t, receiveAll(t, client, client.Queues.EventsURL, 2*time.Second), w.ID); len(early) != 0 {
		t.Fatalf("events published before the crash: %v", early)
	}

	startRecoveryInstance(t)
	waitFor(t, 40*time.Second, "events published by another instance", func() bool {
		_, published, _ := outboxOf(t, e, w.ID)
		return published == 2
	})
	if _, _, reclaimed := outboxOf(t, e, w.ID); reclaimed != 1 {
		t.Errorf("reclaimed events = %d, want the one claimed by the crashed instance", reclaimed)
	}
	ids := eventIDs(t, receiveAll(t, client, client.Queues.EventsURL, 3*time.Second), w.ID)
	if len(ids) != 2 {
		t.Errorf("published eventIds = %v, want the 2 events of the wallet", ids)
	}
	assertConverged(t, e)
}

func TestFault_PublisherCrashesBetweenPublishAndMark(t *testing.T) {
	e := setup(t)
	client := newSQSClient(t)
	assertConverged(t, e)
	purge(t, client, client.Queues.EventsURL)

	runFaultyAlone(t, fault.OutboxAfterPublishBeforeMark)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	waitCrash(t)

	if _, published, _ := outboxOf(t, e, w.ID); published != 0 {
		t.Fatalf("%d events marked as published, want 0 (crashed before marking)", published)
	}

	startRecoveryInstance(t)
	waitFor(t, 40*time.Second, "events marked as published by another instance", func() bool {
		_, published, _ := outboxOf(t, e, w.ID)
		return published == 2
	})
	if _, _, reclaimed := outboxOf(t, e, w.ID); reclaimed != 1 {
		t.Errorf("reclaimed events = %d, want the one published by the crashed instance", reclaimed)
	}

	ids := eventIDs(t, receiveAll(t, client, client.Queues.EventsURL, 3*time.Second), w.ID)
	if len(ids) != 2 {
		t.Errorf("distinct eventIds on the queue = %v, want 2: the republication must keep the eventId", ids)
	}
	t.Logf("copies per eventId on the queue (the FIFO deduplication absorbs the republication): %v", ids)
	assertConverged(t, e)
}

func TestFault_ProcessCrashesBeforeCommit(t *testing.T) {
	e := setup(t)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	assertConverged(t, e)

	runFaultyAlone(t, fault.WagerBeforeCommit)
	id := "e2e-" + uuid.NewString()
	req, _ := json.Marshal(wagerBody(w, "BET", "40.00", id, ""))
	httpReq, _ := http.NewRequest(http.MethodPost, e.instance(app1)+"/wagering/transactions", strings.NewReader(string(req)))
	httpReq.Header.Set("Authorization", "Bearer "+e.ProviderA)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", "provider-a:"+id)
	if resp, err := http.DefaultClient.Do(httpReq); err == nil {
		resp.Body.Close()
		t.Fatalf("got HTTP %d, want the connection to drop with the process", resp.StatusCode)
	}
	waitCrash(t)

	if n := count(t, e.DB, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1`, id); n != 0 {
		t.Fatalf("%d transactions persisted, want none (crashed before commit)", n)
	}
	if debits(t, e, w.ID) != 0 || balance(t, e, w.ID) != 10000 {
		t.Fatalf("crash before commit moved money: %d debits, balance %d", debits(t, e, w.ID), balance(t, e, w.ID))
	}

	startRecoveryInstance(t)
	res := submit(t, e, app2, w, "BET", "40.00", id, "")
	if res.Status != http.StatusCreated || res.Body["idempotentReplay"] == true || res.balance() != "60.00" {
		t.Fatalf("resend after the crash = %d %s, want a first-time 201", res.Status, res.Raw)
	}
	assertConverged(t, e)
}

func TestFault_WorkerCrashesAfterClaimingPendingOperation(t *testing.T) {
	e := setup(t)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	assertConverged(t, e)

	runFaultyAlone(t, fault.WorkerAfterClaim)
	betID := "e2e-" + uuid.NewString()
	refund := submit(t, e, app1, w, "REFUND", "30.00", "e2e-"+uuid.NewString(), betID)
	if refund.Status != http.StatusAccepted {
		t.Fatalf("refund before its bet = %d %s", refund.Status, refund.Raw)
	}
	if bet := submit(t, e, app1, w, "BET", "30.00", betID, ""); bet.Status != http.StatusCreated {
		t.Fatalf("bet = %d %s", bet.Status, bet.Raw)
	}
	waitCrash(t)

	refundID := refund.Body["transactionId"].(string)
	status := func() string {
		var s string
		_ = e.DB.QueryRow(t.Context(), `SELECT status FROM wager_transactions WHERE id = $1`, refundID).Scan(&s)
		return s
	}
	if s := status(); s != "PENDING_REFERENCE" {
		t.Fatalf("refund = %s after the crash, want PENDING_REFERENCE (the claim must roll back)", s)
	}

	startRecoveryInstance(t)
	waitFor(t, 30*time.Second, "pending refund resumed by another instance", func() bool { return status() == "PROCESSED" })
	if balance(t, e, w.ID) != 10000 {
		t.Errorf("balance = %d, want 10000 after BET and REFUND", balance(t, e, w.ID))
	}
	assertConverged(t, e)
}

func TestFault_AllInstancesKilledWithPendingWork(t *testing.T) {
	e := setup(t)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	betID, otherBet := "e2e-"+uuid.NewString(), "e2e-"+uuid.NewString()
	first := submit(t, e, app1, w, "BET", "10.00", otherBet, "")
	refund := submit(t, e, app2, w, "REFUND", "30.00", "e2e-"+uuid.NewString(), betID)
	if first.Status != http.StatusCreated || refund.Status != http.StatusAccepted {
		t.Fatalf("setup = %d / %d", first.Status, refund.Status)
	}

	t.Cleanup(func() { restoreAllInstances(t) })
	compose(t, nil, "kill", "app", "app2", "app3")
	restoreAllInstances(t)

	if replay := submit(t, e, 2, w, "BET", "10.00", otherBet, ""); replay.Status != http.StatusOK ||
		replay.Body["transactionId"] != first.Body["transactionId"] || replay.balance() != "90.00" {
		t.Errorf("replay after the crash = %d %s", replay.Status, replay.Raw)
	}
	if bet := submit(t, e, app1, w, "BET", "30.00", betID, ""); bet.Status != http.StatusCreated {
		t.Fatalf("referenced bet = %d %s", bet.Status, bet.Raw)
	}
	refundID := refund.Body["transactionId"].(string)
	waitFor(t, 30*time.Second, "pending refund resumed after the crash", func() bool {
		return doRequest(t, http.MethodGet, e.instance(2)+"/wagering/transactions/"+refundID, e.ProviderA, nil, nil).Body["status"] == "PROCESSED"
	})
	if balance(t, e, w.ID) != 9000 {
		t.Errorf("balance = %d, want 9000", balance(t, e, w.ID))
	}
	assertConverged(t, e)
}

func TestFault_PostgresTemporarilyUnavailable(t *testing.T) {
	e := setup(t)
	client := newSQSClient(t)
	w := openWallet(t, e, uuid.NewString(), "100.00")
	assertConverged(t, e)

	compose(t, nil, "pause", "postgres")
	t.Cleanup(func() { unpause("postgres") })

	httpID, sqsID := "e2e-"+uuid.NewString(), "e2e-"+uuid.NewString()
	started := time.Now()
	res := submit(t, e, app1, w, "BET", "10.00", httpID, "")
	if res.Status != http.StatusServiceUnavailable {
		t.Errorf("BET while postgres is paused = %d %s, want 503", res.Status, res.Raw)
	}
	t.Logf("503 after %s", time.Since(started).Round(time.Millisecond))
	for i := range e.Instances {
		if r := doRequest(t, http.MethodGet, e.instance(i)+"/health/ready", "", nil, nil); r.Status != http.StatusServiceUnavailable {
			t.Errorf("%s readiness = %d, want 503", e.instance(i), r.Status)
		}
	}
	messageID := sendWager(t, client, w, "BET", "20.00", sqsID)
	time.Sleep(3 * time.Second)

	compose(t, nil, "unpause", "postgres")

	waitFor(t, 60*time.Second, "SQS message processed after postgres is back", func() bool {
		return count(t, e.DB, `SELECT count(*) FROM inbox_messages WHERE message_id = $1 AND completed_at IS NOT NULL`, messageID) == 1
	})
	if retry := submit(t, e, app2, w, "BET", "10.00", httpID, ""); retry.Status != http.StatusCreated && retry.Status != http.StatusOK {
		t.Errorf("HTTP retry after postgres is back = %d %s", retry.Status, retry.Raw)
	}
	if debits(t, e, w.ID) != 2 || balance(t, e, w.ID) != 7000 {
		t.Errorf("%d debits, balance %d; want 2 and 7000", debits(t, e, w.ID), balance(t, e, w.ID))
	}
	for i := range e.Instances {
		if r := doRequest(t, http.MethodGet, e.instance(i)+"/health/ready", "", nil, nil); r.Status != http.StatusOK {
			t.Errorf("%s readiness after recovery = %d", e.instance(i), r.Status)
		}
	}
	assertConverged(t, e)
}

func TestFault_SQSTemporarilyUnavailable(t *testing.T) {
	e := setup(t)
	assertConverged(t, e)

	compose(t, nil, "pause", "localstack")
	t.Cleanup(func() { unpause("localstack") })

	w := openWallet(t, e, uuid.NewString(), "100.00")
	if res := submit(t, e, app2, w, "BET", "10.00", "e2e-"+uuid.NewString(), ""); res.Status != http.StatusCreated {
		t.Fatalf("BET with SQS down = %d %s, want 201 (the outbox decouples publication)", res.Status, res.Raw)
	}
	time.Sleep(8 * time.Second)
	if total, published, _ := outboxOf(t, e, w.ID); published != 0 || total != 4 {
		t.Fatalf("outbox = %d events, %d published; want 4 pending while SQS is down", total, published)
	}
	if backlog := metricOf(t, e.instance(app1), "outbox_pending_events"); backlog < 4 {
		t.Errorf("outbox_pending_events = %v, want >= 4", backlog)
	}
	if r := doRequest(t, http.MethodGet, e.instance(app1)+"/health/ready", "", nil, nil); r.Status != http.StatusServiceUnavailable {
		t.Errorf("readiness with SQS down = %d, want 503", r.Status)
	}

	compose(t, nil, "unpause", "localstack")
	waitFor(t, 90*time.Second, "outbox drained after SQS is back", func() bool {
		_, published, _ := outboxOf(t, e, w.ID)
		return published == 4
	})
	assertConverged(t, e)
}
