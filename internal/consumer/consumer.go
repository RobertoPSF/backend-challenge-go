package consumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/metrics"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/sqsclient"
	"github.com/RobertoPSF/backend-challenge-go/internal/worker"
)

var Module = fx.Module("consumer", fx.Provide(New), fx.Invoke(register))

const messageType = "WagerTransactionRequested"

type envelope struct {
	MessageID  string       `json:"messageId"`
	Type       string       `json:"type"`
	OccurredAt string       `json:"occurredAt"`
	Data       *messageData `json:"data"`
}

type messageData struct {
	domain.WagerRequestInput
	IdempotencyKey string `json:"idempotencyKey"`
}

type outcome int

const (
	outcomeDelete outcome = iota
	outcomeDeadLetter
	outcomeRetry
)

type Consumer struct {
	sqs     *sqsclient.Client
	wagers  *app.Wagers
	cfg     config.Consumer
	metrics *metrics.Metrics
	log     *slog.Logger
}

func New(client *sqsclient.Client, wagers *app.Wagers, cfg config.Config, m *metrics.Metrics, log *slog.Logger) *Consumer {
	return &Consumer{sqs: client, wagers: wagers, cfg: cfg.Consumer, metrics: m, log: log.With("consumer", cfg.Consumer.Name)}
}

func register(lc fx.Lifecycle, c *Consumer, cfg config.Config, log *slog.Logger) {
	if !cfg.Consumer.Enabled {
		log.Info("sqs consumer disabled")
		return
	}
	runner := worker.NewRunner(c, cfg.Consumer.Workers, time.Second, log)
	lc.Append(fx.Hook{OnStart: runner.Start, OnStop: runner.Stop})
}

func (c *Consumer) Name() string { return "sqs-consumer" }

func (c *Consumer) RunOnce(ctx context.Context) (bool, error) {
	out, err := c.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(c.sqs.Queues.InputURL),
		MaxNumberOfMessages:         10,
		WaitTimeSeconds:             int32(c.cfg.WaitTime.Seconds()),
		VisibilityTimeout:           int32(c.cfg.VisibilityTimeout.Seconds()),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount, types.MessageSystemAttributeNameMessageGroupId},
	})
	if ctx.Err() != nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("receive messages: %w", err)
	}

	for i, m := range out.Messages {
		if ctx.Err() != nil {
			c.release(out.Messages[i:])
			break
		}
		c.handle(ctx, m)
	}
	return len(out.Messages) > 0, nil
}

func (c *Consumer) handle(ctx context.Context, m types.Message) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.HandlerTimeout)
	defer cancel()

	log := c.log.With("sqsMessageId", aws.ToString(m.MessageId))
	result, decision, reason := c.process(work, m, log)

	switch decision {
	case outcomeDelete:
		c.delete(work, m, log)
		if result.Duplicate {
			c.metrics.SQSMessages.WithLabelValues("duplicate").Inc()
			log.Info("duplicate message ignored")
		} else {
			c.metrics.SQSMessages.WithLabelValues("processed").Inc()
		}
	case outcomeDeadLetter:
		log.Warn("message sent to dead letter queue", "reason", reason)
		if err := c.deadLetter(work, m, reason); err != nil {
			log.Error("dead letter failed, message will be redelivered", "error", err)
			return
		}
		c.delete(work, m, log)
		c.metrics.SQSMessages.WithLabelValues("dead_letter").Inc()
		c.metrics.SQSDeadLetters.WithLabelValues(reasonCode(reason)).Inc()
	case outcomeRetry:
		c.metrics.SQSMessages.WithLabelValues("retry").Inc()
		delay := c.retryDelay(receiveCount(m))
		log.Warn("transient failure, message will be retried", "reason", reason, "retryIn", delay.String())
		c.changeVisibility(work, m, delay)
	}
}

func (c *Consumer) process(ctx context.Context, m types.Message, log *slog.Logger) (app.MessageResult, outcome, string) {
	body := aws.ToString(m.Body)
	env, err := decode(body)
	if err != nil {
		return app.MessageResult{}, outcomeDeadLetter, "INVALID_MESSAGE: " + err.Error()
	}
	log = log.With("messageId", env.MessageID, "providerId", env.Data.ProviderID)
	if !slices.Contains(c.cfg.KnownProviders, env.Data.ProviderID) {
		return app.MessageResult{}, outcomeDeadLetter, "UNKNOWN_PROVIDER"
	}
	req, err := domain.ParseWagerRequest(env.Data.WagerRequestInput, env.Data.IdempotencyKey)
	if err != nil {
		decision, reason := decide(err)
		return app.MessageResult{}, decision, reason
	}

	result, err := c.wagers.ProcessMessage(ctx,
		app.InboundMessage{Consumer: c.cfg.Name, MessageID: env.MessageID, Hash: messageHash(req)},
		app.WagerCommand{Request: req, CorrelationID: env.MessageID, CausationID: env.MessageID, Channel: app.ChannelSQS})
	if err != nil {
		decision, reason := decide(err)
		return app.MessageResult{}, decision, reason
	}
	if !result.Duplicate {
		s := result.Transaction.Snapshot()
		log.Info("wager message handled", "transactionId", s.ID, "walletId", s.WalletID, "kind", s.Kind,
			"status", s.Status, "failureCode", s.FailureCode, "idempotentReplay", result.Replay)
	}
	return result, outcomeDelete, ""
}

func decode(body string) (envelope, error) {
	dec := json.NewDecoder(bytes.NewBufferString(body))
	dec.DisallowUnknownFields()
	var env envelope
	if err := dec.Decode(&env); err != nil {
		return envelope{}, fmt.Errorf("malformed JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return envelope{}, errors.New("body must contain a single JSON object")
	}
	switch {
	case env.MessageID == "":
		return envelope{}, errors.New("messageId is required")
	case env.Type != messageType:
		return envelope{}, fmt.Errorf("type must be %s", messageType)
	case env.OccurredAt == "":
		return envelope{}, errors.New("occurredAt is required")
	case env.Data == nil:
		return envelope{}, errors.New("data is required")
	}
	return env, nil
}

func messageHash(req domain.WagerRequest) string {
	sum := sha256.Sum256([]byte(req.PayloadHash() + ":" + req.IdempotencyKey))
	return hex.EncodeToString(sum[:])
}

func decide(err error) (outcome, string) {
	var de *domain.DomainError
	switch {
	case errors.As(err, &de) && (de.Kind == domain.KindValidation || de.Kind == domain.KindConflict):
		return outcomeDeadLetter, string(de.Code)
	default:
		return outcomeRetry, err.Error()
	}
}

func (c *Consumer) retryDelay(receiveCount int) time.Duration {
	delay := c.cfg.RetryBaseDelay
	for i := 1; i < receiveCount && delay < c.cfg.RetryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, c.cfg.RetryMaxDelay)
}

func receiveCount(m types.Message) int {
	n, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	return max(n, 1)
}

func (c *Consumer) delete(ctx context.Context, m types.Message, log *slog.Logger) {
	_, err := c.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.sqs.Queues.InputURL), ReceiptHandle: m.ReceiptHandle})
	if err != nil {
		log.Error("delete failed, message will be redelivered and deduplicated by the inbox", "error", err)
	}
}

func (c *Consumer) deadLetter(ctx context.Context, m types.Message, reason string) error {
	group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "invalid"
	}
	_, err := c.sqs.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.sqs.Queues.InputDLQURL),
		MessageBody:            m.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: m.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			"failureReason":   {DataType: aws.String("String"), StringValue: aws.String(truncate(reason, 256))},
			"sourceMessageId": {DataType: aws.String("String"), StringValue: m.MessageId},
		},
	})
	return err
}

func (c *Consumer) changeVisibility(ctx context.Context, m types.Message, delay time.Duration) {
	_, err := c.sqs.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.sqs.Queues.InputURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: int32(delay.Seconds()),
	})
	if err != nil {
		c.log.Error("change visibility failed", "error", err)
	}
}

func (c *Consumer) release(messages []types.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, m := range messages {
		c.changeVisibility(ctx, m, 0)
	}
	c.log.Info("released unprocessed messages on shutdown", "count", len(messages))
}

func reasonCode(reason string) string {
	code, _, _ := strings.Cut(reason, ":")
	return code
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
