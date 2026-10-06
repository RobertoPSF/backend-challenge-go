package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.uber.org/fx"
)

var Module = fx.Module("metrics", fx.Provide(
	NewRegistry,
	func(reg *prometheus.Registry) prometheus.Registerer { return reg },
	New,
))

func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

type Metrics struct {
	WagerTransactions        *prometheus.CounterVec
	IdempotentReplays        *prometheus.CounterVec
	ProcessingDuration       *prometheus.HistogramVec
	ConcurrencyConflicts     *prometheus.CounterVec
	SQSMessages              *prometheus.CounterVec
	SQSDeadLetters           *prometheus.CounterVec
	PendingReferenceAttempts *prometheus.CounterVec
	OutboxPublish            *prometheus.CounterVec
	ReconciliationMismatches prometheus.Counter
	HTTPRequests             *prometheus.HistogramVec
}

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		WagerTransactions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_transactions_total",
			Help: "Wager operations handled, by channel (http, sqs, worker), kind and resulting status.",
		}, []string{"channel", "kind", "status"}),
		IdempotentReplays: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_idempotent_replays_total",
			Help: "Repeated operations answered with the persisted result, by channel.",
		}, []string{"channel"}),
		ProcessingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wager_processing_duration_seconds",
			Help:    "Time to process a wager operation, including the database transaction, by channel.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"channel"}),
		ConcurrencyConflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_concurrency_conflicts_total",
			Help: "Transactions retried because of a concurrency conflict, by reason.",
		}, []string{"reason"}),
		SQSMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sqs_messages_total",
			Help: "Inbound SQS messages by outcome: processed, duplicate, dead_letter, retry.",
		}, []string{"result"}),
		SQSDeadLetters: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sqs_dead_letters_total",
			Help: "Inbound messages sent to the dead letter queue by the consumer, by failure code.",
		}, []string{"reason"}),
		PendingReferenceAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pending_reference_attempts_total",
			Help: "Evaluations of operations waiting for a reference, by outcome: resolved, rescheduled, expired.",
		}, []string{"outcome"}),
		OutboxPublish: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_publish_total",
			Help: "Outbox publication attempts by result: published, failed.",
		}, []string{"result"}),
		ReconciliationMismatches: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_mismatches_total",
			Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
		}),
		HTTPRequests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP requests by route pattern, method and status code.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
	}
	reg.MustRegister(m.WagerTransactions, m.IdempotentReplays, m.ProcessingDuration, m.ConcurrencyConflicts,
		m.SQSMessages, m.SQSDeadLetters, m.PendingReferenceAttempts, m.OutboxPublish, m.ReconciliationMismatches, m.HTTPRequests)
	return m
}
