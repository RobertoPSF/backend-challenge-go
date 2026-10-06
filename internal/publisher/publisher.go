package publisher

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
	"github.com/RobertoPSF/backend-challenge-go/internal/worker"
)

var Module = fx.Module("publisher", fx.Provide(New), fx.Invoke(register))

type Publisher struct {
	store    *store.Store
	sqs      *sqsclient.Client
	cfg      config.Outbox
	instance string
	metrics  *metrics.Metrics
	log      *slog.Logger
	now      func() time.Time
}

func New(st *store.Store, client *sqsclient.Client, cfg config.Config, m *metrics.Metrics, log *slog.Logger) *Publisher {
	return &Publisher{store: st, sqs: client, cfg: cfg.Outbox, instance: cfg.InstanceID, metrics: m,
		log: log.With("component", "outbox-publisher"), now: time.Now}
}

func register(lc fx.Lifecycle, p *Publisher, cfg config.Config, log *slog.Logger) {
	if !cfg.Outbox.Enabled {
		log.Info("outbox publisher disabled")
		return
	}
	runner := worker.NewRunner(p, cfg.Outbox.Workers, cfg.Outbox.PollInterval, log)
	lc.Append(fx.Hook{OnStart: runner.Start, OnStop: runner.Stop})
}

func (p *Publisher) Name() string { return "outbox-publisher" }

func (p *Publisher) RunOnce(ctx context.Context) (bool, error) {
	owner := p.instance + "/" + uuid.NewString()
	outbox := p.store.Read().Outbox
	events, err := outbox.ClaimBatch(ctx, owner, p.now(), p.cfg.Lease, p.cfg.BatchSize)
	if ctx.Err() != nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim outbox events: %w", err)
	}

	for i, e := range events {
		if ctx.Err() != nil {
			p.release(events[i:], owner)
			break
		}
		p.publish(ctx, e, owner)
	}
	return len(events) > 0, nil
}

func (p *Publisher) publish(ctx context.Context, e store.OutboxEvent, owner string) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.PublishTimeout)
	defer cancel()
	outbox := p.store.Read().Outbox
	log := p.log.With("eventId", e.EventID, "eventType", e.EventType, "walletId", e.AggregateID, "attempt", e.Attempts)

	_, err := p.sqs.SendMessage(work, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.sqs.Queues.EventsURL),
		MessageBody:            aws.String(string(e.Payload)),
		MessageGroupId:         aws.String(e.AggregateID.String()),
		MessageDeduplicationId: aws.String(e.EventID.String()),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType":    {DataType: aws.String("String"), StringValue: aws.String(e.EventType)},
			"eventVersion": {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(e.Version))},
		},
	})
	if err != nil {
		p.metrics.OutboxPublish.WithLabelValues("failed").Inc()
		next := p.now().Add(p.retryDelay(e.Attempts))
		log.Warn("event publication failed, will retry", "error", err, "nextAttemptAt", next)
		if err := outbox.MarkFailed(work, e.EventID, owner, next, truncate(err.Error(), 1000)); err != nil {
			log.Error("could not record publication failure; the lease will expire", "error", err)
		}
		return
	}
	p.metrics.OutboxPublish.WithLabelValues("published").Inc()
	if err := outbox.MarkPublished(work, e.EventID, owner, p.now()); err != nil {
		log.Error("event published but not marked; it will be republished with the same eventId", "error", err)
		return
	}
	log.Debug("event published")
}

func (p *Publisher) retryDelay(attempts int) time.Duration {
	delay := p.cfg.RetryBaseDelay
	for i := 1; i < attempts && delay < p.cfg.RetryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, p.cfg.RetryMaxDelay)
}

func (p *Publisher) release(events []store.OutboxEvent, owner string) {
	ids := make([]uuid.UUID, len(events))
	for i, e := range events {
		ids[i] = e.EventID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.Read().Outbox.Release(ctx, ids, owner); err != nil {
		p.log.Error("could not release claimed events; their lease will expire", "error", err)
		return
	}
	p.log.Info("released claimed events on shutdown", "count", len(ids))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
