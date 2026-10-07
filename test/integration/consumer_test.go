//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx/fxtest"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/consumer"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

type consumerEnv struct {
	wagerEnv
	sqs *sqsclient.Client
	cfg config.Config
}

func newConsumerEnv(t *testing.T) consumerEnv {
	t.Helper()
	ls := testinfra.StartLocalStack(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	cfg := pendingConfig
	cfg.AWS = config.AWS{Region: "us-east-1", EndpointURL: ls.Endpoint,
		InputQueue: "wager-transactions.fifo", EventsQueue: "wallet-events.fifo", InputDLQ: "wager-transactions-dlq.fifo"}
	cfg.Consumer = config.Consumer{Name: "wallet-service", Workers: 1, WaitTime: time.Second, VisibilityTimeout: 5 * time.Second,
		HandlerTimeout: 4 * time.Second, RetryBaseDelay: time.Second, RetryMaxDelay: 2 * time.Second,
		KnownProviders: []string{"provider-a", "provider-b"}}

	lc := fxtest.NewLifecycle(t)
	client, err := sqsclient.New(lc, cfg, silentLog)
	if err != nil {
		t.Fatal(err)
	}
	lc.RequireStart()
	t.Cleanup(lc.RequireStop)

	return consumerEnv{wagerEnv: newWagerEnvWith(t, 1, cfg), sqs: client, cfg: cfg}
}

func (e consumerEnv) consumerFor(wagers *app.Wagers) *consumer.Consumer {
	return consumer.New(e.sqs, wagers, e.cfg, e.envs[0].metrics, silentLog)
}

func (e consumerEnv) send(t *testing.T, body string, groupID string) {
	t.Helper()
	_, err := e.sqs.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(e.sqs.Queues.InputURL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(groupID), MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e consumerEnv) drain(t *testing.T, c *consumer.Consumer, rounds int) {
	t.Helper()
	for range rounds {
		if _, err := c.RunOnce(context.Background()); err != nil {
			t.Fatalf("run once: %v", err)
		}
	}
}

func (e consumerEnv) queueSize(t *testing.T, url string) int {
	t.Helper()
	out, err := e.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible, _ := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)])
	hidden, _ := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)])
	return visible + hidden
}

func (e consumerEnv) deadLetters(t *testing.T) []types.Message {
	t.Helper()
	out, err := e.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(e.sqs.Queues.InputDLQURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
		MessageAttributeNames: []string{"All"}, VisibilityTimeout: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out.Messages {
		_, _ = e.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(e.sqs.Queues.InputDLQURL), ReceiptHandle: m.ReceiptHandle})
	}
	return out.Messages
}

func reasonOf(m types.Message) string {
	if a, ok := m.MessageAttributes["failureReason"]; ok {
		return aws.ToString(a.StringValue)
	}
	return ""
}

func message(t *testing.T, messageID string, w *domain.Wallet, kind, amount, externalID, reference string) string {
	t.Helper()
	data := map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID, "idempotencyKey": "provider-a:" + externalID,
		"playerId": w.PlayerID().String(), "walletId": w.ID().String(), "roundId": "round-1", "gameId": "game-1",
		"kind": kind, "money": map[string]any{"amount": amount, "currency": "BRL"},
	}
	if reference != "" {
		data["referenceExternalTransactionId"] = reference
	}
	raw, _ := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00.000Z", "data": data,
	})
	return string(raw)
}

func TestSQSConsumer(t *testing.T) {
	env := newConsumerEnv(t)
	c := env.consumerFor(env.instances[0])
	id := func(prefix string) string { return prefix + "-" + uuid.NewString() }

	t.Run("valid message is processed, recorded in the inbox and deleted", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		msgID, ext := id("msg"), id("bet")
		env.send(t, message(t, msgID, w, "BET", "25.00", ext, ""), w.ID().String())
		env.drain(t, c, 2)

		if balance, _ := env.walletState(t, w.ID()); balance != 7500 {
			t.Fatalf("balance = %d, want 7500", balance)
		}
		if n := count(t, env.db, `SELECT count(*) FROM inbox_messages i JOIN wager_transactions t ON t.id = i.transaction_id
			WHERE i.message_id = $1 AND i.completed_at IS NOT NULL AND t.external_transaction_id = $2 AND t.correlation_id = $1`, msgID, ext); n != 1 {
			t.Errorf("completed inbox rows = %d", n)
		}
		if n := env.queueSize(t, env.sqs.Queues.InputURL); n != 0 {
			t.Errorf("queue size = %d, want 0", n)
		}
	})

	t.Run("redelivery of the same message has a single effect", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		body := message(t, id("msg"), w, "BET", "10.00", id("bet"), "")
		env.send(t, body, w.ID().String())
		env.send(t, body, w.ID().String())
		env.drain(t, c, 3)

		if balance, _ := env.walletState(t, w.ID()); balance != 9000 {
			t.Fatalf("balance = %d, want 9000 (debited once)", balance)
		}
		if n := count(t, env.db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID()); n != 1 {
			t.Errorf("debits = %d", n)
		}
		if n := env.queueSize(t, env.sqs.Queues.InputURL); n != 0 {
			t.Errorf("queue size = %d, want 0", n)
		}
	})

	t.Run("same messageId with another payload goes to the DLQ", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		msgID := id("msg")
		env.send(t, message(t, msgID, w, "BET", "10.00", id("bet"), ""), w.ID().String())
		env.drain(t, c, 2)
		env.send(t, message(t, msgID, w, "BET", "99.00", id("bet"), ""), w.ID().String())
		env.drain(t, c, 2)

		dlq := env.deadLetters(t)
		if len(dlq) != 1 || reasonOf(dlq[0]) != "MESSAGE_ID_CONFLICT" {
			t.Fatalf("dlq = %d messages, reason %q", len(dlq), reasonOf(dlq[0]))
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 9000 {
			t.Errorf("balance = %d, want 9000", balance)
		}
	})

	t.Run("permanently invalid messages go straight to the DLQ with the reason", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		ghost := *w
		unknownWallet := message(t, id("msg"), &ghost, "BET", "1.00", id("bet"), "")
		var m map[string]any
		_ = json.Unmarshal([]byte(unknownWallet), &m)
		m["data"].(map[string]any)["walletId"] = uuid.NewString()
		raw, _ := json.Marshal(m)
		unknownProvider := message(t, id("msg"), w, "BET", "1.00", id("bet"), "")
		_ = json.Unmarshal([]byte(unknownProvider), &m)
		m["data"].(map[string]any)["providerId"] = "provider-x"
		rawProvider, _ := json.Marshal(m)

		cases := map[string]string{
			"not json":         `{"messageId":`,
			"wrong type":       `{"messageId":"m","type":"Other","occurredAt":"x","data":{}}`,
			"invalid amount":   message(t, id("msg"), w, "BET", "1e3", id("bet"), ""),
			"unknown wallet":   string(raw),
			"unknown provider": string(rawProvider),
			"opening kind":     message(t, id("msg"), w, "OPENING", "1.00", id("bet"), ""),
		}
		want := map[string]string{
			"not json": "INVALID_MESSAGE", "wrong type": "INVALID_MESSAGE", "invalid amount": "INVALID_MONEY",
			"unknown wallet": "WALLET_NOT_FOUND", "unknown provider": "UNKNOWN_PROVIDER", "opening kind": "UNSUPPORTED_KIND",
		}
		for name, body := range cases {
			t.Run(name, func(t *testing.T) {
				env.send(t, body, "g-"+strings.ReplaceAll(name, " ", "-"))
				env.drain(t, c, 2)
				dlq := env.deadLetters(t)
				if len(dlq) != 1 || len(reasonOf(dlq[0])) < len(want[name]) || reasonOf(dlq[0])[:len(want[name])] != want[name] {
					t.Fatalf("dlq = %d, reason %q, want %s", len(dlq), reasonOf(dlq[0]), want[name])
				}
				if aws.ToString(dlq[0].Body) != body {
					t.Error("DLQ must keep the original body")
				}
			})
		}
		if n := env.queueSize(t, env.sqs.Queues.InputURL); n != 0 {
			t.Errorf("queue size = %d, want 0", n)
		}
		if n := count(t, env.db, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND origin = 'EXTERNAL'`, w.ID()); n != 0 {
			t.Errorf("invalid messages persisted %d transactions", n)
		}
	})

	t.Run("business rejection and pending reference are terminal for the queue", func(t *testing.T) {
		w := env.openWallet(t, "10.00")
		rejectedExt, pendingExt := id("bet"), id("refund")
		env.send(t, message(t, id("msg"), w, "BET", "50.00", rejectedExt, ""), w.ID().String())
		env.send(t, message(t, id("msg"), w, "REFUND", "5.00", pendingExt, "bet-later"), w.ID().String())
		env.drain(t, c, 3)

		var rejected, pending string
		_ = env.db.QueryRow(context.Background(), `SELECT status FROM wager_transactions WHERE external_transaction_id = $1`, rejectedExt).Scan(&rejected)
		_ = env.db.QueryRow(context.Background(), `SELECT status FROM wager_transactions WHERE external_transaction_id = $1`, pendingExt).Scan(&pending)
		if rejected != "REJECTED" || pending != "PENDING_REFERENCE" {
			t.Fatalf("statuses = %s / %s", rejected, pending)
		}
		if n := env.queueSize(t, env.sqs.Queues.InputURL); n != 0 {
			t.Errorf("queue size = %d, want 0", n)
		}
		if n := len(env.deadLetters(t)); n != 0 {
			t.Errorf("dlq = %d, want 0", n)
		}
	})

	t.Run("the same operation over HTTP and SQS has a single effect", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		ext := id("bet")
		if _, err := env.instances[0].Process(context.Background(), wagerCmd(t, w, "BET", "20.00", ext)); err != nil {
			t.Fatal(err)
		}
		env.send(t, message(t, id("msg"), w, "BET", "20.00", ext, ""), w.ID().String())
		env.drain(t, c, 2)

		if balance, _ := env.walletState(t, w.ID()); balance != 8000 {
			t.Fatalf("balance = %d, want 8000", balance)
		}
		if n := env.queueSize(t, env.sqs.Queues.InputURL); n != 0 {
			t.Errorf("queue size = %d, want 0 (replay deletes the message)", n)
		}
	})

	t.Run("consumer metrics count processed, duplicate and dead-lettered messages", func(t *testing.T) {
		reg := env.envs[0].reg
		for result, min := range map[string]float64{"processed": 1, "duplicate": 1, "dead_letter": 1} {
			if v := metricValue(t, reg, "sqs_messages_total", "result", result); v < min {
				t.Errorf("sqs_messages_total{result=%q} = %v", result, v)
			}
		}
		for _, reason := range []string{"MESSAGE_ID_CONFLICT", "INVALID_MESSAGE", "UNKNOWN_PROVIDER", "WALLET_NOT_FOUND"} {
			if v := metricValue(t, reg, "sqs_dead_letters_total", "reason", reason); v < 1 {
				t.Errorf("sqs_dead_letters_total{reason=%q} = %v", reason, v)
			}
		}
		if v := metricValue(t, reg, "wager_idempotent_replays_total", "channel", "sqs"); v < 1 {
			t.Errorf("sqs replays = %v", v)
		}
	})

	t.Run("transient failure is retried with backoff and then succeeds", func(t *testing.T) {
		broken := env.consumerFor(brokenWagers(t, env.cfg))
		w := env.openWallet(t, "100.00")
		env.send(t, message(t, id("msg"), w, "BET", "30.00", id("bet"), ""), w.ID().String())

		env.drain(t, broken, 1)
		if balance, _ := env.walletState(t, w.ID()); balance != 10000 {
			t.Fatalf("broken consumer changed the balance: %d", balance)
		}
		if n := env.queueSize(t, env.sqs.Queues.InputURL); n != 1 {
			t.Fatalf("queue size = %d, want 1 (message kept for retry)", n)
		}

		deadline := time.Now().Add(10 * time.Second)
		for env.queueSize(t, env.sqs.Queues.InputURL) > 0 && time.Now().Before(deadline) {
			env.drain(t, c, 1)
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 7000 {
			t.Fatalf("balance = %d, want 7000 after recovery", balance)
		}
	})

	t.Run("transient failures beyond maxReceiveCount reach the DLQ through redrive", func(t *testing.T) {
		broken := env.consumerFor(brokenWagers(t, env.cfg))
		w := env.openWallet(t, "100.00")
		body := message(t, id("msg"), w, "BET", "30.00", id("bet"), "")
		env.send(t, body, w.ID().String())

		deadline := time.Now().Add(30 * time.Second)
		for env.queueSize(t, env.sqs.Queues.InputURL) > 0 && time.Now().Before(deadline) {
			env.drain(t, broken, 1)
		}
		dlq := env.deadLetters(t)
		if len(dlq) != 1 || aws.ToString(dlq[0].Body) != body || reasonOf(dlq[0]) != "" {
			t.Fatalf("dlq = %d messages, want the original message moved by redrive", len(dlq))
		}
		if balance, _ := env.walletState(t, w.ID()); balance != 10000 {
			t.Errorf("balance = %d, want 10000", balance)
		}
	})
}

func brokenWagers(t *testing.T, cfg config.Config) *app.Wagers {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m := metrics.New(prometheus.NewRegistry())
	cfg.Database.TxTimeout = 15 * time.Second
	return app.NewWagers(store.New(pool, cfg, m, silentLog), cfg, m)
}
