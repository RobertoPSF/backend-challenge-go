//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
	"github.com/RobertoPSF/backend-challenge-go/internal/publisher"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
)

type publishedEvent struct {
	EventID     string `json:"eventId"`
	EventType   string `json:"eventType"`
	AggregateID string `json:"aggregateId"`
	Data        struct {
		WalletVersion int64 `json:"walletVersion"`
	} `json:"data"`
	GroupID  string
	TypeAttr string
	DedupID  string
}

func (e consumerEnv) publisher(t *testing.T, client *sqsclient.Client) *publisher.Publisher {
	t.Helper()
	cfg := e.cfg
	cfg.InstanceID = "test-" + uuid.NewString()[:8]
	cfg.Outbox = config.Outbox{Workers: 1, BatchSize: 50, Lease: 2 * time.Second, PollInterval: 50 * time.Millisecond,
		PublishTimeout: time.Second, RetryBaseDelay: 300 * time.Millisecond, RetryMaxDelay: time.Second}
	return publisher.New(e.envs[0].store, client, cfg, silentLog)
}

func publishAll(t *testing.T, p *publisher.Publisher) {
	t.Helper()
	for {
		did, err := p.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
}

func (e consumerEnv) receiveEvents(t *testing.T) []publishedEvent {
	t.Helper()
	var all []publishedEvent
	for {
		out, err := e.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(e.sqs.Queues.EventsURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames: []string{"All"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameMessageDeduplicationId,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Messages) == 0 {
			return all
		}
		for _, m := range out.Messages {
			var ev publishedEvent
			if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &ev); err != nil {
				t.Fatalf("event body is not JSON: %v", err)
			}
			ev.GroupID = m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
			ev.DedupID = m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)]
			ev.TypeAttr = aws.ToString(m.MessageAttributes["eventType"].StringValue)
			all = append(all, ev)
			_, _ = e.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(e.sqs.Queues.EventsURL), ReceiptHandle: m.ReceiptHandle})
		}
	}
}

func eventsOf(events []publishedEvent, walletID uuid.UUID) []publishedEvent {
	var out []publishedEvent
	for _, e := range events {
		if e.AggregateID == walletID.String() {
			out = append(out, e)
		}
	}
	return out
}

func TestOutboxPublisher(t *testing.T) {
	env := newConsumerEnv(t)
	ctx := context.Background()
	id := func(prefix string) string { return prefix + "-" + uuid.NewString() }

	t.Run("committed events are published after the commit, routed by wallet and marked", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		if _, err := env.instances[0].Process(ctx, wagerCmd(t, w, "BET", "10.00", id("bet"))); err != nil {
			t.Fatal(err)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND published_at IS NULL`, w.ID()); n != 4 {
			t.Fatalf("pending events before publishing = %d, want 4", n)
		}

		publishAll(t, env.publisher(t, env.sqs))
		events := eventsOf(env.receiveEvents(t), w.ID())
		if len(events) != 4 {
			t.Fatalf("published events = %d, want 4", len(events))
		}
		for _, e := range events {
			if e.GroupID != w.ID().String() || e.DedupID != e.EventID || e.TypeAttr != e.EventType {
				t.Errorf("routing = group %s dedup %s type %s for %+v", e.GroupID, e.DedupID, e.TypeAttr, e)
			}
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND published_at IS NOT NULL
			AND locked_by IS NULL AND attempts = 1`, w.ID()); n != 4 {
			t.Errorf("marked events = %d, want 4", n)
		}
	})

	t.Run("events of a wallet are published in order across concurrent publishers", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		for range 6 {
			if _, err := env.instances[0].Process(ctx, wagerCmd(t, w, "BET", "1.00", id("bet"))); err != nil {
				t.Fatal(err)
			}
		}
		publishers := []*publisher.Publisher{env.publisher(t, env.sqs), env.publisher(t, env.sqs), env.publisher(t, env.sqs)}
		var wg sync.WaitGroup
		for i := range 6 {
			wg.Go(func() { publishAll(t, publishers[i%3]) })
		}
		wg.Wait()

		var versions []int64
		for _, e := range eventsOf(env.receiveEvents(t), w.ID()) {
			if e.EventType == "WalletBalanceChanged" {
				versions = append(versions, e.Data.WalletVersion)
			}
		}
		want := []int64{1, 2, 3, 4, 5, 6, 7}
		if len(versions) != len(want) {
			t.Fatalf("balance events = %v, want versions %v", versions, want)
		}
		for i := range want {
			if versions[i] != want[i] {
				t.Fatalf("published order = %v, want %v", versions, want)
			}
		}
	})

	t.Run("concurrent publishers publish every event exactly once", func(t *testing.T) {
		var wallets []uuid.UUID
		for range 10 {
			w := env.openWallet(t, "100.00")
			wallets = append(wallets, w.ID())
			for range 2 {
				if _, err := env.instances[0].Process(ctx, wagerCmd(t, w, "BET", "1.00", id("bet"))); err != nil {
					t.Fatal(err)
				}
			}
		}
		publishers := []*publisher.Publisher{env.publisher(t, env.sqs), env.publisher(t, env.sqs), env.publisher(t, env.sqs)}
		var wg sync.WaitGroup
		for i := range 9 {
			wg.Go(func() { publishAll(t, publishers[i%3]) })
		}
		wg.Wait()

		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = ANY($1) AND (published_at IS NULL OR attempts <> 1)`, wallets); n != 0 {
			t.Errorf("events not published exactly once = %d", n)
		}
		seen := map[string]int{}
		for _, e := range env.receiveEvents(t) {
			seen[e.EventID]++
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = ANY($1)`, wallets); len(seen) < n {
			t.Errorf("distinct events received = %d, want %d", len(seen), n)
		}
	})

	t.Run("broker failure is retried with backoff and holds the later events of that wallet", func(t *testing.T) {
		broken := *env.sqs
		broken.Queues.EventsURL = env.sqs.Queues.EventsURL + "-missing"
		failing := env.publisher(t, &broken)

		w := env.openWallet(t, "100.00")
		publishAll(t, failing)

		var attempts int
		var lastError *string
		var next time.Time
		_ = env.db.QueryRow(ctx, `SELECT attempts, last_error, next_attempt_at FROM outbox_events
			WHERE aggregate_id = $1 ORDER BY occurred_at, event_id LIMIT 1`, w.ID()).Scan(&attempts, &lastError, &next)
		if attempts != 1 || lastError == nil || !next.After(time.Now()) {
			t.Fatalf("after failure: attempts=%d lastError=%v next=%s", attempts, lastError, next)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND attempts > 0`, w.ID()); n != 1 {
			t.Errorf("claimed events of the wallet = %d, want only the head (1)", n)
		}

		time.Sleep(400 * time.Millisecond)
		publishAll(t, env.publisher(t, env.sqs))
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND published_at IS NOT NULL`, w.ID()); n != 2 {
			t.Fatalf("published after recovery = %d, want 2", n)
		}
		if events := eventsOf(env.receiveEvents(t), w.ID()); len(events) != 2 || events[0].EventType != "WagerTransactionProcessed" {
			t.Errorf("events after recovery = %+v", events)
		}
	})

	t.Run("crash between publication and confirmation: another instance republishes with the same eventId", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		outbox := env.envs[0].store.Read().Outbox
		claimed, err := outbox.ClaimBatch(ctx, "crashed-instance", time.Now(), time.Second, 100)
		if err != nil {
			t.Fatal(err)
		}
		var head store.OutboxEvent
		for _, e := range claimed {
			if e.AggregateID == w.ID() {
				head = e
			}
		}
		if head.EventID == uuid.Nil {
			t.Fatal("head event of the wallet was not claimed")
		}
		if _, err := env.sqs.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl: aws.String(env.sqs.Queues.EventsURL), MessageBody: aws.String(string(head.Payload)),
			MessageGroupId: aws.String(w.ID().String()), MessageDeduplicationId: aws.String(head.EventID.String()),
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := env.publisher(t, env.sqs).RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NULL`, head.EventID); n != 1 {
			t.Fatal("a live lease must not be taken over")
		}

		time.Sleep(1100 * time.Millisecond)
		publishAll(t, env.publisher(t, env.sqs))
		var attempts int
		var published *time.Time
		_ = env.db.QueryRow(ctx, `SELECT attempts, published_at FROM outbox_events WHERE event_id = $1`, head.EventID).Scan(&attempts, &published)
		if published == nil || attempts != 2 {
			t.Fatalf("after recovery: published=%v attempts=%d, want published on the 2nd attempt", published, attempts)
		}
		for _, e := range eventsOf(env.receiveEvents(t), w.ID()) {
			if e.EventType == "WagerTransactionProcessed" && e.EventID != head.EventID.String() {
				t.Errorf("republished event changed id: %s != %s", e.EventID, head.EventID)
			}
		}
	})

	t.Run("crash between commit and publication: pending events are taken over", func(t *testing.T) {
		w := env.openWallet(t, "100.00")
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND published_at IS NULL`, w.ID()); n != 2 {
			t.Fatalf("pending = %d", n)
		}
		publishAll(t, env.publisher(t, env.sqs))
		if n := count(t, env.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND published_at IS NULL`, w.ID()); n != 0 {
			t.Errorf("still pending = %d", n)
		}
		env.receiveEvents(t)
	})
}
